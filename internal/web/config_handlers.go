package web

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
)

const testConnectionTimeout = 10 * time.Second

type mediaServerView struct {
	ID                int64
	Name, Type, URL   string
	SkipLocalPlayback bool
	Enabled           bool
	HasCredential     bool
	Sessions          int
	Dot               statusDot
}

type downloaderView struct {
	ID              int64
	Name, Type, URL string
	// Username is round-tripped so an edit doesn't blank it; the
	// password never is.
	Username                                 string
	HasCredential                            bool
	DefaultDownloadLimit, DefaultUploadLimit int64
	BackupDownloadLimit, BackupUploadLimit   int64
	Enabled                                  bool
	Dot                                      statusDot
}

// statusDot is the dot before a name in the Connections tables, and
// the two lines of the hover card it opens.
type statusDot struct {
	Tone   string // good | warn | critical | faint
	Label  string
	Detail string
}

func (s *Server) serverDot(ms db.MediaServer, sessions int) statusDot {
	switch {
	case !ms.Enabled:
		return statusDot{"faint", "Disabled", "Not watched for playback"}
	case s.watchers != nil && !s.watchers.Running(ms.ID):
		return statusDot{"critical", "Unreachable", "Unable to connect"}
	case sessions > 0:
		return statusDot{"good", "Connected", count(sessions, "active session", "active sessions")}
	default:
		return statusDot{"good", "Connected", "No activity detected"}
	}
}

// downloaderDot reads the scheduler's view of one downloader. A failed
// apply reads Unreachable, never Default rates.
func (s *Server) downloaderDot(dl db.Downloader) statusDot {
	switch state := s.scheduler.State(dl.ID); {
	case !dl.Enabled:
		return statusDot{"faint", "Disabled", "Excluded from throttling"}
	case state.Failure != "":
		return statusDot{"critical", "Unreachable", "Unable to connect"}
	case !state.Known:
		return statusDot{"faint", "Unknown", "State not resolved yet"}
	case s.scheduler.Paused():
		return statusDot{"good", "Default rates", "Throttling disabled"}
	case state.Throttled:
		return statusDot{"warn", "Throttled", state.Cause + " is playing"}
	default:
		return statusDot{"good", "Default rates", "Nothing mapped is playing"}
	}
}

// authSummary states how login is configured. Read-only: there is no
// password change in the browser.
func (s *Server) authSummary() string {
	switch s.auth.Mode() {
	case auth.ModeDisabled:
		return "disabled · anyone who can reach this app is signed in"
	case auth.ModeEnvironment:
		return "signed in as " + s.auth.Username() + " · credentials from the environment"
	default:
		return "signed in as " + s.auth.Username()
	}
}

func mappingKey(mediaServerID, downloaderID int64) string {
	return strconv.FormatInt(mediaServerID, 10) + ":" + strconv.FormatInt(downloaderID, 10)
}

type configPageData struct {
	pageData
	Tab               string
	MediaServers      []mediaServerView
	Downloaders       []downloaderView
	AuthSummary       string
	AuthNote          string
	Update            updateView
	MappingOn         map[string]bool
	About             aboutView
	Uptime            string
	EditMediaServerID int64 // 0 means "add" mode
	EditMediaServer   mediaServerView
	EditDownloaderID  int64
	EditDownloader    downloaderView
	FormError         string
	Sheet             string // drawer contents: "server", "downloader", or "" when shut
	OOB               bool   // render the Connections tables for an out-of-band swap
}

func toMediaServerView(ms db.MediaServer) mediaServerView {
	return mediaServerView{
		ID:                ms.ID,
		Name:              ms.Name,
		Type:              ms.Type,
		URL:               ms.URL,
		SkipLocalPlayback: ms.SkipLocalPlayback,
		Enabled:           ms.Enabled,
		HasCredential:     ms.CredentialEncrypted != "",
	}
}

func (s *Server) toDownloaderView(dl db.Downloader) downloaderView {
	username, _, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		slog.Warn("reading downloader username for the form", "downloader", dl.Name, "error", err)
	}
	return downloaderView{
		Username:             username,
		ID:                   dl.ID,
		Name:                 dl.Name,
		Type:                 dl.Type,
		URL:                  dl.URL,
		HasCredential:        dl.CredentialEncrypted.Valid && dl.CredentialEncrypted.String != "",
		DefaultDownloadLimit: dl.DefaultDownloadLimit,
		DefaultUploadLimit:   dl.DefaultUploadLimit,
		BackupDownloadLimit:  dl.BackupDownloadLimit,
		BackupUploadLimit:    dl.BackupUploadLimit,
		Enabled:              dl.Enabled,
	}
}

func (s *Server) buildConfigPageData(r *http.Request, tab string) (configPageData, error) {
	mediaServers, err := s.db.ListMediaServers()
	if err != nil {
		return configPageData{}, err
	}
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		return configPageData{}, err
	}
	mappings, err := s.db.ListMappings()
	if err != nil {
		return configPageData{}, err
	}

	msViews := make([]mediaServerView, 0, len(mediaServers))
	for _, ms := range mediaServers {
		view := toMediaServerView(ms)
		view.Sessions = s.scheduler.ActiveSessionCount(ms.ID)
		view.Dot = s.serverDot(ms, view.Sessions)
		msViews = append(msViews, view)
	}

	dlViews := make([]downloaderView, 0, len(downloaders))
	for _, dl := range downloaders {
		view := s.toDownloaderView(dl)
		view.Dot = s.downloaderDot(dl)
		dlViews = append(dlViews, view)
	}

	enabled := make(map[string]bool, len(mappings))
	for _, m := range mappings {
		enabled[mappingKey(m.MediaServerID, m.DownloaderID)] = m.Enabled
	}

	return configPageData{
		pageData:     s.page(r, "Configuration", "configuration"),
		Tab:          tab,
		MediaServers: msViews,
		Downloaders:  dlViews,
		MappingOn:    enabled,
		About:        s.aboutView(),
		Uptime:       uptimeSince(s.startedAt),
		AuthSummary:  s.authSummary(),
		AuthNote:     authNote(runtime.GOOS),
		Update:       idleUpdate(),
	}, nil
}

func (s *Server) renderConfigError(w http.ResponseWriter, r *http.Request, message string) {
	// From the drawer, the error lands above the form and leaves it as it was.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "#sheet-error")
		w.Header().Set("HX-Reswap", "innerHTML")
		s.renderFragment(w, "configuration", "sheeterror", message)
		return
	}
	data, err := s.buildConfigPageData(r, "connections")
	if err != nil {
		slog.Error("building configuration page after form error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.FormError = message
	w.WriteHeader(http.StatusUnprocessableEntity)
	s.render(w, "configuration", data)
}

func (s *Server) configurationPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tab := q.Get("tab")
	// The old media-servers and downloaders tabs, edit links included,
	// land on Connections.
	var sheet string
	switch tab {
	case "media-servers":
		sheet = "server"
	case "downloaders":
		sheet = "downloader"
	}
	if tab == "" || sheet != "" {
		tab = "connections"
	}

	data, err := s.buildConfigPageData(r, tab)
	if err != nil {
		slog.Error("building configuration page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if id, err := strconv.ParseInt(q.Get("edit"), 10, 64); err == nil && sheet != "" {
		if !s.fillSheet(&data, sheet, id) {
			data.Sheet = ""
		}
	}
	s.render(w, "configuration", data)
}

// fillSheet loads the drawer's form: blank for an id of 0, otherwise the
// entry being edited. It reports false when there is no such entry.
func (s *Server) fillSheet(data *configPageData, sheet string, id int64) bool {
	data.Sheet = sheet
	// New entries start enabled, and named after their default type.
	data.EditMediaServer = mediaServerView{Name: "Plex", Enabled: true}
	data.EditDownloader = downloaderView{Name: "qBittorrent", Enabled: true}
	if id == 0 {
		return true
	}
	switch sheet {
	case "server":
		ms, err := s.db.GetMediaServer(id)
		if err != nil {
			return false
		}
		data.EditMediaServerID, data.EditMediaServer = ms.ID, toMediaServerView(ms)
	case "downloader":
		dl, err := s.db.GetDownloader(id)
		if err != nil {
			return false
		}
		data.EditDownloaderID, data.EditDownloader = dl.ID, s.toDownloaderView(dl)
	}
	return true
}

// sheetForm serves the drawer's add or edit form on its own.
func (s *Server) sheetForm(sheet string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var id int64
		if raw := r.PathValue("id"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				http.Error(w, "bad id", http.StatusBadRequest)
				return
			}
			id = parsed
		}
		data := configPageData{pageData: s.page(r, "Configuration", "configuration")}
		if !s.fillSheet(&data, sheet, id) {
			http.NotFound(w, r)
			return
		}
		s.renderFragment(w, "configuration", "sheetbody", data)
	}
}

// connectionStatus serves the Connections tables' dots and session
// counts, for the page to merge in place.
func (s *Server) connectionStatus(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildConfigPageData(r, "connections")
	if err != nil {
		slog.Error("building connection status", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "configuration", "connectionstatus", data)
}

// savedConnection answers a successful save. From the drawer, the tables
// are swapped out of band and the drawer told to close; a plain post is
// redirected.
func (s *Server) savedConnection(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/configuration?tab=connections", http.StatusSeeOther)
		return
	}
	data, err := s.buildConfigPageData(r, "connections")
	if err != nil {
		slog.Error("rebuilding connections after save", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.OOB = true
	w.Header().Set("HX-Trigger", "at-sheet-close")
	w.Header().Set("HX-Reswap", "none")
	s.renderFragment(w, "configuration", "connections", data)
}

func parseInt64Form(r *http.Request, name string) int64 {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// --- media servers ---

// mediaServerCredential picks the credential the user actually filled in.
// Each credential style has its own field, and hidden panes stay in the
// DOM, so a shared name would let a blank field beat the filled one.
func mediaServerCredential(r *http.Request, msType string) string {
	if msType != "plex" {
		return r.FormValue("api_token")
	}
	if pasted := r.FormValue("token"); pasted != "" {
		return pasted
	}
	return r.FormValue("plex_token")
}

func (s *Server) saveMediaServer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	msType := r.FormValue("type")
	rawURL := strings.TrimSpace(r.FormValue("url"))
	token := mediaServerCredential(r, msType)
	skipLocal := r.FormValue("skip_local_playback") == "on"
	enabled := r.FormValue("enabled") == "on"

	if name == "" || rawURL == "" || !clients.SupportedMediaServerType(msType) {
		s.renderConfigError(w, r, "Name, URL, and a supported type are required.")
		return
	}

	var id int64
	if idStr := r.FormValue("id"); idStr != "" {
		parsed, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		id = parsed
	}

	if clash := s.duplicateMediaServer(id, name, rawURL); clash != "" {
		s.renderConfigError(w, r, clash)
		return
	}

	credentialEncrypted := ""
	if id != 0 {
		existing, err := s.db.GetMediaServer(id)
		if err != nil {
			slog.Error("loading media server for update", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		credentialEncrypted = existing.CredentialEncrypted
		if token == "" && credentialEncrypted != "" && !sameOrigin(existing.URL, rawURL) {
			s.renderConfigError(w, r, "Enter the token again for the new address. A saved token is only ever sent to the address it was saved for.")
			return
		}
	}
	if token != "" {
		encrypted, err := crypto.Encrypt(s.encryptionKey, token)
		if err != nil {
			slog.Error("encrypting media server token", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		credentialEncrypted = encrypted
	}
	if credentialEncrypted == "" {
		s.renderConfigError(w, r, "A token is required.")
		return
	}

	ms := db.MediaServer{
		ID:                  id,
		Name:                name,
		Type:                msType,
		URL:                 rawURL,
		CredentialEncrypted: credentialEncrypted,
		SkipLocalPlayback:   skipLocal,
		Enabled:             enabled,
	}

	var err error
	if id == 0 {
		var newID int64
		newID, err = s.db.CreateMediaServer(ms)
		if err == nil {
			// Not fatal: the server was created.
			if seedErr := s.db.EnableMappingsForMediaServer(newID); seedErr != nil {
				slog.Error("seeding mappings for new media server", "error", seedErr)
			}
		}
	} else {
		err = s.db.UpdateMediaServer(ms)
	}
	if err != nil {
		slog.Error("saving media server", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(true)
	s.savedConnection(w, r)
}

func (s *Server) deleteMediaServer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteMediaServer(id); err != nil {
		slog.Error("deleting media server", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(true)
	http.Redirect(w, r, "/configuration?tab=connections", http.StatusSeeOther)
}

// storedMediaServerCredential returns the saved credential of the server
// being edited, only while the form keeps the address it was saved for:
// otherwise anyone signed in could read it out by pointing the address at
// their own machine and pressing Test.
func (s *Server) storedMediaServerCredential(r *http.Request, rawURL string) (string, bool) {
	idStr := r.FormValue("id")
	if idStr == "" {
		return "", false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", false
	}
	ms, err := s.db.GetMediaServer(id)
	if err != nil || ms.CredentialEncrypted == "" || !sameOrigin(ms.URL, rawURL) {
		return "", false
	}
	token, err := crypto.Decrypt(s.encryptionKey, ms.CredentialEncrypted)
	if err != nil {
		return "", false
	}
	return token, true
}

// storedDownloaderCredential is the same for a downloader: its saved
// password, when the form keeps the saved username and address.
func (s *Server) storedDownloaderCredential(r *http.Request, rawURL, username string) (string, bool) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		return "", false
	}
	dl, err := s.db.GetDownloader(id)
	if err != nil || !dl.CredentialEncrypted.Valid || !sameOrigin(dl.URL, rawURL) {
		return "", false
	}
	storedUser, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil || storedUser != username || password == "" {
		return "", false
	}
	return password, true
}

// sameOrigin reports whether two URLs share scheme, host and port, so a
// credential saved for one may be sent to the other. The path may differ.
func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		port(ua) == port(ub)
}

// probe runs one connection test and writes its result.
func (s *Server) probe(w http.ResponseWriter, r *http.Request, kind probeKind, target, okMessage string, test func(context.Context) error) {
	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := test(ctx); err != nil {
		field, hint := classifyProbeError(kind, err)
		s.writeTestResult(w, testResult{Target: target, Message: clients.Redact(err).Error(), Fields: field, Hint: hint, Elapsed: time.Since(started)})
		return
	}
	s.writeTestResult(w, testResult{OK: true, Target: target, Message: okMessage, Elapsed: time.Since(started)})
}

func (s *Server) testMediaServerForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.writeTestResult(w, testResult{Message: "bad form data"})
		return
	}
	msType := r.FormValue("type")
	name := strings.TrimSpace(r.FormValue("name"))
	rawURL := strings.TrimSpace(r.FormValue("url"))
	token := mediaServerCredential(r, msType)
	// Editing: a blank credential means the stored one.
	if token == "" {
		if stored, ok := s.storedMediaServerCredential(r, rawURL); ok {
			token = stored
		}
	}
	if fields, hint := missingRequired(name, rawURL, token, true); fields != nil {
		s.writeTestResult(w, testResult{Message: "Nothing to test", Fields: fields, Hint: hint})
		return
	}
	if !clients.SupportedMediaServerType(msType) {
		s.writeTestResult(w, testResult{Message: "choose a supported media server type first"})
		return
	}
	s.probe(w, r, probeServer, rawURL, "Successfully connected.", func(ctx context.Context) error {
		return clients.TestMediaServer(ctx, msType, rawURL, token)
	})
}

func (s *Server) testMediaServerByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeTestResult(w, testResult{Message: "bad id"})
		return
	}
	ms, err := s.db.GetMediaServer(id)
	if err != nil {
		s.writeTestResult(w, testResult{Message: "media server not found"})
		return
	}
	token, err := crypto.Decrypt(s.encryptionKey, ms.CredentialEncrypted)
	if err != nil {
		s.writeTestResult(w, testResult{Message: "stored credential could not be decrypted", Fields: []string{fieldCredential}, Hint: "Re-enter the token and save the server again."})
		return
	}
	s.probe(w, r, probeServer, ms.URL, "Successfully connected.", func(ctx context.Context) error {
		return clients.TestMediaServer(ctx, ms.Type, ms.URL, token)
	})
}

// --- downloaders ---

func (s *Server) saveDownloader(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	dlType := r.FormValue("type")
	rawURL := strings.TrimSpace(r.FormValue("url"))
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	enabled := r.FormValue("enabled") == "on"

	if name == "" || rawURL == "" || !clients.SupportedDownloaderType(dlType) {
		s.renderConfigError(w, r, "Name, URL, and a supported type are required.")
		return
	}

	var id int64
	if idStr := r.FormValue("id"); idStr != "" {
		parsed, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		id = parsed
	}

	if clash := s.duplicateDownloader(id, name, rawURL); clash != "" {
		s.renderConfigError(w, r, clash)
		return
	}

	credentialEncrypted := sql.NullString{}
	if id != 0 {
		existing, err := s.db.GetDownloader(id)
		if err != nil {
			slog.Error("loading downloader for update", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		credentialEncrypted = existing.CredentialEncrypted
	}
	switch {
	case r.FormValue("clear_credentials") == "on":
		credentialEncrypted = sql.NullString{}
	case password != "":
		if username == "" {
			s.renderConfigError(w, r, "Enter the username that goes with the password.")
			return
		}
		encrypted, err := credential.Encrypt(s.encryptionKey, username, password)
		if err != nil {
			slog.Error("encrypting downloader credential", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		credentialEncrypted = sql.NullString{String: encrypted, Valid: true}
	case credentialEncrypted.Valid:
		// A blank password keeps the saved one, for the saved username
		// and address only.
		if _, ok := s.storedDownloaderCredential(r, rawURL, username); !ok {
			s.renderConfigError(w, r, "Enter the password again. A saved password is only ever used with the username and address it was saved for.")
			return
		}
	case username != "":
		s.renderConfigError(w, r, "Enter the password that goes with the username.")
		return
	}

	dl := db.Downloader{
		ID:                   id,
		Name:                 name,
		Type:                 dlType,
		URL:                  rawURL,
		CredentialEncrypted:  credentialEncrypted,
		DefaultDownloadLimit: parseInt64Form(r, "default_download_limit"),
		DefaultUploadLimit:   parseInt64Form(r, "default_upload_limit"),
		BackupDownloadLimit:  parseInt64Form(r, "backup_download_limit"),
		BackupUploadLimit:    parseInt64Form(r, "backup_upload_limit"),
		Enabled:              enabled,
	}

	var err error
	if id == 0 {
		var newID int64
		newID, err = s.db.CreateDownloader(dl)
		if err == nil {
			if seedErr := s.db.EnableMappingsForDownloader(newID); seedErr != nil {
				slog.Error("seeding mappings for new downloader", "error", seedErr)
			}
		}
	} else {
		err = s.db.UpdateDownloader(dl)
	}
	if err != nil {
		slog.Error("saving downloader", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(false)
	s.savedConnection(w, r)
}

func (s *Server) deleteDownloader(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteDownloader(id); err != nil {
		slog.Error("deleting downloader", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(false)
	http.Redirect(w, r, "/configuration?tab=connections", http.StatusSeeOther)
}

func (s *Server) testDownloaderForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.writeTestResult(w, testResult{Message: "bad form data"})
		return
	}
	dlType := r.FormValue("type")
	name := strings.TrimSpace(r.FormValue("name"))
	rawURL := strings.TrimSpace(r.FormValue("url"))
	// Credentials are optional: a WebUI may have authentication off.
	if fields, hint := missingRequired(name, rawURL, "", false); fields != nil {
		s.writeTestResult(w, testResult{Message: "Nothing to test", Fields: fields, Hint: hint})
		return
	}
	if !clients.SupportedDownloaderType(dlType) {
		s.writeTestResult(w, testResult{Message: "choose a supported downloader type first"})
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	// Editing: a blank password means the saved one, for the saved
	// username and address only.
	if password == "" {
		if stored, ok := s.storedDownloaderCredential(r, rawURL, username); ok {
			password = stored
		}
	}

	s.probe(w, r, probeDownloader, rawURL, "Successfully connected.", func(ctx context.Context) error {
		if err := clients.TestDownloader(ctx, dlType, rawURL, username, password); err != nil {
			return err
		}
		// A failing saved downloader is reachable again: re-apply now.
		if id, err := strconv.ParseInt(r.FormValue("id"), 10, 64); err == nil {
			s.scheduler.RetryIfFailing(id)
		}
		return nil
	})
}

func (s *Server) testDownloaderByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeTestResult(w, testResult{Message: "bad id"})
		return
	}
	dl, err := s.db.GetDownloader(id)
	if err != nil {
		s.writeTestResult(w, testResult{Message: "downloader not found"})
		return
	}

	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		s.writeTestResult(w, testResult{Message: "stored credential could not be read", Fields: []string{fieldCredential}, Hint: "Re-enter the password and save the client again."})
		return
	}

	s.probe(w, r, probeDownloader, dl.URL, "Successfully connected.", func(ctx context.Context) error {
		if err := clients.TestDownloader(ctx, dl.Type, dl.URL, username, password); err != nil {
			return err
		}
		s.scheduler.RetryIfFailing(dl.ID)
		return nil
	})
}

// --- mappings ---

// setMapping turns one cell of the mappings matrix on or off, addressed
// by pair since a cell has no id until first ticked.
func (s *Server) setMapping(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	serverID, err1 := strconv.ParseInt(r.FormValue("media_server_id"), 10, 64)
	downloaderID, err2 := strconv.ParseInt(r.FormValue("downloader_id"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "bad ids", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1"

	if err := s.db.SetMappingForPair(serverID, downloaderID, enabled); err != nil {
		slog.Error("setting mapping", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(false)

	// Re-render rather than 204: HTMX ignores an empty response, and the
	// cell would keep its old label.
	if r.Header.Get("HX-Request") == "true" {
		data, err := s.buildConfigPageData(r, "mappings")
		if err != nil {
			slog.Error("rebuilding mappings after toggle", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.renderFragment(w, "configuration", "mappingmatrix", data)
		return
	}
	http.Redirect(w, r, "/configuration?tab=mappings", http.StatusSeeOther)
}

// --- shared ---

// applyConfigChange makes a saved change take effect now: media-server
// changes reconcile the watchers, and every change re-resolves throttle
// state, asynchronously so a slow downloader can't stall the response.
func (s *Server) applyConfigChange(mediaServersChanged bool) {
	if mediaServersChanged && s.watchers != nil {
		s.watchers.Reconcile()
	}
	s.scheduler.ResolveAsync()
}

// testResultView is the shape the testresult template renders.
type testResultView struct {
	State       string
	Tone        string
	Icon        string
	Label       string
	Target      string
	Elapsed     string
	Detail      string
	Hint        string
	Fields      []string
	Dismissable bool
}

// minTestDuration holds a fast reply back briefly, so a LAN answer in
// milliseconds still reads as a test having run. The time shown is real.
const minTestDuration = 550 * time.Millisecond

// testResult is one probe's outcome, in the shape the card renders.
type testResult struct {
	OK      bool
	Target  string
	Message string
	Hint    string
	Fields  []string
	Elapsed time.Duration
}

// writeTestResult renders a connection test's result. The backend's own
// error is shown verbatim: it is usually the only thing that says what
// went wrong.
func (s *Server) writeTestResult(w http.ResponseWriter, res testResult) {
	if res.Elapsed > 0 && res.Elapsed < minTestDuration {
		time.Sleep(minTestDuration - res.Elapsed)
	}
	view := testResultView{
		State: "failed", Tone: "critical", Icon: "unreachable", Label: "Failed",
		Target: res.Target, Detail: res.Message, Hint: res.Hint, Fields: res.Fields, Dismissable: true,
	}
	if res.OK {
		view.State, view.Tone, view.Icon, view.Label = "connected", "good", "normal", "Connected"
		view.Fields = nil
	}
	if res.Elapsed > 0 {
		view.Elapsed = fmt.Sprintf("%.1fs", res.Elapsed.Seconds())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderFragment(w, "configuration", "testresult", view)
}
