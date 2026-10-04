package web

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
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
	// Sessions is the live count, so the table answers "is this server
	// doing anything" without a trip to the Dashboard.
	Sessions int
}

type downloaderView struct {
	ID              int64
	Name, Type, URL string
	// Username is not a secret and is round-tripped so an edit doesn't
	// silently blank it. The password never is — see CredentialField.
	Username                                 string
	HasCredential                            bool
	DefaultDownloadLimit, DefaultUploadLimit int64
	BackupDownloadLimit, BackupUploadLimit   int64
	Enabled                                  bool
	// State is the downloader's live throttle state, for the table's
	// "Now" column.
	State string
}

// downloaderState collapses the scheduler's view into the one word the
// tables and rows render. A failed apply reads Unreachable, never
// Normal: the rates on the client are not the rates this app believes
// it set.
func (s *Server) downloaderState(dl db.Downloader) string {
	switch state := s.scheduler.State(dl.ID); {
	case !dl.Enabled:
		return "disabled"
	case state.Failure != "":
		return "unreachable"
	case !state.Known:
		return "unknown"
	case s.scheduler.Paused():
		return "paused"
	case state.Throttled:
		return "throttled"
	default:
		return "normal"
	}
}

// authSummary states how login is configured. Read-only on purpose:
// there is no password change in the browser, and a field here would
// imply there is.
func (s *Server) authSummary() string {
	switch s.auth.Mode() {
	case auth.ModeDisabled:
		return "disabled — anyone who can reach this app is signed in"
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
	Update            updateView
	MappingOn         map[string]bool
	About             aboutView
	Uptime            string
	EditMediaServerID int64 // 0 means "add" mode
	EditMediaServer   mediaServerView
	EditDownloaderID  int64
	EditDownloader    downloaderView
	FormError         string
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
	// Only the username is read back. The password is never rendered,
	// not even masked at its real length.
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
		msViews = append(msViews, view)
	}

	dlViews := make([]downloaderView, 0, len(downloaders))
	for _, dl := range downloaders {
		view := s.toDownloaderView(dl)
		view.State = s.downloaderState(dl)
		dlViews = append(dlViews, view)
	}

	// The matrix asks "is this pair on?" per cell, so the mappings are
	// indexed by pair rather than scanned per cell.
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
		Update:       idleUpdate(),
	}, nil
}

func (s *Server) renderConfigError(w http.ResponseWriter, r *http.Request, tab, message string) {
	data, err := s.buildConfigPageData(r, tab)
	if err != nil {
		slog.Error("building configuration page after form error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.FormError = message
	w.WriteHeader(http.StatusUnprocessableEntity)
	if err := s.templates["configuration"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering configuration page", "error", err)
	}
}

func (s *Server) configurationPage(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "media-servers"
	}

	data, err := s.buildConfigPageData(r, tab)
	if err != nil {
		slog.Error("building configuration page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// A new server or downloader starts switched on. Left at the zero
	// value, the box rendered unticked, so anyone who didn't notice it
	// saved one that was never watched or throttled, with no error
	// anywhere to say why nothing happened.
	data.EditMediaServer.Enabled = true
	data.EditDownloader.Enabled = true

	if editID := r.URL.Query().Get("edit"); editID != "" {
		if id, err := strconv.ParseInt(editID, 10, 64); err == nil {
			switch tab {
			case "media-servers":
				if ms, err := s.db.GetMediaServer(id); err == nil {
					data.EditMediaServerID = ms.ID
					data.EditMediaServer = toMediaServerView(ms)
				}
			case "downloaders":
				if dl, err := s.db.GetDownloader(id); err == nil {
					data.EditDownloaderID = dl.ID
					data.EditDownloader = s.toDownloaderView(dl)
				}
			}
		}
	}

	if err := s.templates["configuration"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering configuration page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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
//
// The form carries a field per credential style — a pasted Plex token, a
// token handed over by Plex sign-in, an Emby or Jellyfin API key — and
// the panes the user is not looking at stay in the DOM. Reading a single
// shared name would let whichever blank field came first in the document
// silently beat the one they filled in, which is exactly the bug that
// made saving a Plex server after signing in do nothing at all.
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
		s.renderConfigError(w, r, "media-servers", "Name, URL, and a supported type are required.")
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
		s.renderConfigError(w, r, "media-servers", clash)
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
		s.renderConfigError(w, r, "media-servers", "A token is required.")
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
			// Seeding is a convenience, not part of the save. Failing the
			// whole request here would report an error for a server that
			// had in fact been created.
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

	http.Redirect(w, r, "/configuration?tab=media-servers", http.StatusSeeOther)
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
	http.Redirect(w, r, "/configuration?tab=media-servers", http.StatusSeeOther)
}

// storedMediaServerCredential returns the credential already saved for
// the server this form is editing, if there is one.
func (s *Server) storedMediaServerCredential(r *http.Request) (string, bool) {
	idStr := r.FormValue("id")
	if idStr == "" {
		return "", false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", false
	}
	ms, err := s.db.GetMediaServer(id)
	if err != nil || ms.CredentialEncrypted == "" {
		return "", false
	}
	token, err := crypto.Decrypt(s.encryptionKey, ms.CredentialEncrypted)
	if err != nil {
		return "", false
	}
	return token, true
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
	// Editing an existing server: an untouched credential field means
	// "keep what is stored", so the test uses the stored one rather than
	// demanding it be retyped.
	if token == "" {
		if stored, ok := s.storedMediaServerCredential(r); ok {
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
	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestMediaServer(ctx, msType, rawURL, token); err != nil {
		field, hint := classifyProbeError(probeServer, err)
		s.writeTestResult(w, testResult{Target: rawURL, Message: err.Error(), Fields: field, Hint: hint, Elapsed: time.Since(started)})
		return
	}
	s.writeTestResult(w, testResult{OK: true, Target: rawURL, Message: "Reachable, and the credential was accepted.", Elapsed: time.Since(started)})
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
	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestMediaServer(ctx, ms.Type, ms.URL, token); err != nil {
		field, hint := classifyProbeError(probeServer, err)
		s.writeTestResult(w, testResult{Target: ms.URL, Message: err.Error(), Fields: field, Hint: hint, Elapsed: time.Since(started)})
		return
	}
	s.writeTestResult(w, testResult{OK: true, Target: ms.URL, Message: "Reachable, and the credential was accepted.", Elapsed: time.Since(started)})
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
		s.renderConfigError(w, r, "downloaders", "Name, URL, and a supported type are required.")
		return
	}
	if (username == "") != (password == "") {
		s.renderConfigError(w, r, "downloaders", "Both username and password are required to set downloader credentials.")
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
		s.renderConfigError(w, r, "downloaders", clash)
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
	case username != "" && password != "":
		encrypted, err := credential.Encrypt(s.encryptionKey, username, password)
		if err != nil {
			slog.Error("encrypting downloader credential", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		credentialEncrypted = sql.NullString{String: encrypted, Valid: true}
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

	http.Redirect(w, r, "/configuration?tab=downloaders", http.StatusSeeOther)
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
	http.Redirect(w, r, "/configuration?tab=downloaders", http.StatusSeeOther)
}

func (s *Server) testDownloaderForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.writeTestResult(w, testResult{Message: "bad form data"})
		return
	}
	dlType := r.FormValue("type")
	name := strings.TrimSpace(r.FormValue("name"))
	rawURL := strings.TrimSpace(r.FormValue("url"))
	// A downloader's credentials are genuinely optional — a WebUI with
	// authentication switched off is a legitimate setup — so only the
	// name and the URL are required here.
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

	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestDownloader(ctx, dlType, rawURL, username, password); err != nil {
		field, hint := classifyProbeError(probeDownloader, err)
		s.writeTestResult(w, testResult{Target: rawURL, Message: err.Error(), Fields: field, Hint: hint, Elapsed: time.Since(started)})
		return
	}
	s.writeTestResult(w, testResult{OK: true, Target: rawURL, Message: reachedMessage(username != "" || password != ""), Elapsed: time.Since(started)})
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

	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestDownloader(ctx, dl.Type, dl.URL, username, password); err != nil {
		field, hint := classifyProbeError(probeDownloader, err)
		s.writeTestResult(w, testResult{Target: dl.URL, Message: err.Error(), Fields: field, Hint: hint, Elapsed: time.Since(started)})
		return
	}
	s.writeTestResult(w, testResult{OK: true, Target: dl.URL, Message: reachedMessage(username != "" || password != ""), Elapsed: time.Since(started)})
}

// --- mappings ---

// setMapping turns one cell of the mappings matrix on or off. A cell
// has no id until it is first ticked, so this addresses a mapping by
// the pair it joins rather than by id.
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

	// Re-render the matrix rather than answering 204. HTMX does nothing
	// with an empty response, so the cell kept its old label and the
	// toggle read as broken even though the mapping had changed.
	if r.Header.Get("HX-Request") == "true" {
		data, err := s.buildConfigPageData(r, "mappings")
		if err != nil {
			slog.Error("rebuilding mappings after toggle", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := s.templates["configuration"].ExecuteTemplate(w, "mappingmatrix", data); err != nil {
			slog.Error("rendering mappings matrix", "error", err)
		}
		return
	}
	http.Redirect(w, r, "/configuration?tab=mappings", http.StatusSeeOther)
}

// --- shared ---

// applyConfigChange makes a just-saved configuration change take
// effect immediately. Media-server changes additionally reconcile the
// running watchers (start, stop, or reconnect under new credentials);
// every change re-resolves throttle state, since a new mapping or an
// edited rate has to reach the downloader without waiting for the next
// playback event. Resolution is asynchronous so a slow or unreachable
// downloader can't stall the redirect.
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

// writeTestResult renders a connection test into its bounded box.
//
// The backend's own error string is shown verbatim in mono, wrapped by
// a plain-language headline rather than replaced by one — the machine's
// words are usually the only thing that says what actually went wrong.
// minTestDuration is how long a probe is held before its answer is
// written. A client on the same LAN can answer in single-digit
// milliseconds, which made the result look like it had appeared without
// a test having run. Only the reply waits — the elapsed time on the
// card is the real one, and a probe slower than this is not delayed at
// all. Validation errors, which never reach the network, skip it.
const minTestDuration = 550 * time.Millisecond

// reachedMessage is the success line for a connection test.
//
// A downloader WebUI with authentication switched off is a normal
// setup on a home network, and claiming a credential "was accepted"
// when none was sent is simply untrue — nothing was checked.
func reachedMessage(hadCredential bool) string {
	if hadCredential {
		return "Reachable, and the credential was accepted."
	}
	return "Reachable. No credentials were sent, so none were checked."
}

// testResult is one probe's outcome, in the shape the card renders.
type testResult struct {
	OK      bool
	Target  string
	Message string
	Hint    string
	Fields  []string
	Elapsed time.Duration
}

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
	if err := s.templates["configuration"].ExecuteTemplate(w, "testresult", view); err != nil {
		slog.Error("rendering test result", "error", err)
	}
}
