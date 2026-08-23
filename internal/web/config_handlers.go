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
	"github.com/JaiMistry/autothrottle/internal/settings"
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

type mappingView struct {
	ID                              int64
	MediaServerID, DownloaderID     int64
	MediaServerName, DownloaderName string
	Enabled                         bool
}

// settingView renders one runtime knob generically, from the
// definitions the settings package publishes — so a new setting needs
// no template change.
type settingView struct {
	Key         string
	Label       string
	Description string
	Unit        string
	Value       string
	Min, Max    int64
}

func settingViews(store *settings.Store) []settingView {
	current := store.All()
	out := make([]settingView, 0, len(settings.Definitions))
	for _, def := range settings.Definitions {
		value := current[def.Key]
		if value == "" {
			value = def.Default
		}
		out = append(out, settingView{
			Key: def.Key, Label: def.Label, Description: def.Description,
			Unit: def.Unit, Value: value, Min: def.Min, Max: def.Max,
		})
	}
	return out
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
// credentials come from the environment at deploy time, and a field
// here would imply they can be changed from the browser.
func (s *Server) authSummary() string {
	if s.authCfg.Disabled {
		return "disabled — anyone who can reach this app is signed in"
	}
	return "enabled · signed in as " + s.authCfg.Username
}

func mappingKey(mediaServerID, downloaderID int64) string {
	return strconv.FormatInt(mediaServerID, 10) + ":" + strconv.FormatInt(downloaderID, 10)
}

type configPageData struct {
	pageData
	Tab               string
	MediaServers      []mediaServerView
	Downloaders       []downloaderView
	Mappings          []mappingView
	AuthSummary       string
	Update            updateView
	MappingOn         map[string]bool
	Settings          []settingView
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

	msByID := make(map[int64]db.MediaServer, len(mediaServers))
	msViews := make([]mediaServerView, 0, len(mediaServers))
	for _, ms := range mediaServers {
		msByID[ms.ID] = ms
		view := toMediaServerView(ms)
		view.Sessions = s.scheduler.ActiveSessionCount(ms.ID)
		msViews = append(msViews, view)
	}

	dlByID := make(map[int64]db.Downloader, len(downloaders))
	dlViews := make([]downloaderView, 0, len(downloaders))
	for _, dl := range downloaders {
		dlByID[dl.ID] = dl
		view := s.toDownloaderView(dl)
		view.State = s.downloaderState(dl)
		dlViews = append(dlViews, view)
	}

	mapViews := make([]mappingView, 0, len(mappings))
	for _, m := range mappings {
		mv := mappingView{ID: m.ID, MediaServerID: m.MediaServerID, DownloaderID: m.DownloaderID, Enabled: m.Enabled}
		if ms, ok := msByID[m.MediaServerID]; ok {
			mv.MediaServerName = ms.Name
		}
		if dl, ok := dlByID[m.DownloaderID]; ok {
			mv.DownloaderName = dl.Name
		}
		mapViews = append(mapViews, mv)
	}

	sessionToken := auth.SessionTokenFromContext(r.Context())

	// The matrix asks "is this pair on?" per cell, so the mappings are
	// indexed by pair rather than scanned per cell.
	enabled := make(map[string]bool, len(mappings))
	for _, m := range mappings {
		enabled[mappingKey(m.MediaServerID, m.DownloaderID)] = m.Enabled
	}

	_ = sessionToken
	return configPageData{
		pageData:     s.page(r, "Configuration", "configuration"),
		Tab:          tab,
		MediaServers: msViews,
		Downloaders:  dlViews,
		Mappings:     mapViews,
		MappingOn:    enabled,
		Settings:     settingViews(s.settings),
		AuthSummary:  s.authSummary(),
		Update:       unknownUpdate(),
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
			err = s.db.EnableMappingsForMediaServer(newID)
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

func (s *Server) testMediaServerForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.writeTestResult(w, false, "", "bad form data", 0)
		return
	}
	msType := r.FormValue("type")
	rawURL := strings.TrimSpace(r.FormValue("url"))
	token := mediaServerCredential(r, msType)
	if rawURL == "" || token == "" {
		s.writeTestResult(w, false, "", "enter a URL and token first", 0)
		return
	}
	if !clients.SupportedMediaServerType(msType) {
		s.writeTestResult(w, false, "", "choose a supported media server type first", 0)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestMediaServer(ctx, msType, rawURL, token); err != nil {
		s.writeTestResult(w, false, rawURL, err.Error(), time.Since(started))
		return
	}
	s.writeTestResult(w, true, rawURL, "Reachable, and the credential was accepted.", time.Since(started))
}

func (s *Server) testMediaServerByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeTestResult(w, false, "", "bad id", 0)
		return
	}
	ms, err := s.db.GetMediaServer(id)
	if err != nil {
		s.writeTestResult(w, false, "", "media server not found", 0)
		return
	}
	token, err := crypto.Decrypt(s.encryptionKey, ms.CredentialEncrypted)
	if err != nil {
		s.writeTestResult(w, false, "", "stored credential could not be decrypted", 0)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestMediaServer(ctx, ms.Type, ms.URL, token); err != nil {
		s.writeTestResult(w, false, ms.URL, err.Error(), time.Since(started))
		return
	}
	s.writeTestResult(w, true, ms.URL, "Reachable, and the credential was accepted.", time.Since(started))
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
		encoded, err := credential.EncodeDownloader(username, password)
		if err != nil {
			slog.Error("encoding downloader credential", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		encrypted, err := crypto.Encrypt(s.encryptionKey, encoded)
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
			err = s.db.EnableMappingsForDownloader(newID)
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
		s.writeTestResult(w, false, "", "bad form data", 0)
		return
	}
	dlType := r.FormValue("type")
	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		s.writeTestResult(w, false, "", "enter a URL first", 0)
		return
	}
	if !clients.SupportedDownloaderType(dlType) {
		s.writeTestResult(w, false, "", "choose a supported downloader type first", 0)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestDownloader(ctx, dlType, rawURL, username, password); err != nil {
		s.writeTestResult(w, false, rawURL, err.Error(), time.Since(started))
		return
	}
	s.writeTestResult(w, true, rawURL, "Reachable, and the credential was accepted.", time.Since(started))
}

func (s *Server) testDownloaderByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeTestResult(w, false, "", "bad id", 0)
		return
	}
	dl, err := s.db.GetDownloader(id)
	if err != nil {
		s.writeTestResult(w, false, "", "downloader not found", 0)
		return
	}

	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		s.writeTestResult(w, false, "", "stored credential could not be read", 0)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	started := time.Now()
	if err := clients.TestDownloader(ctx, dl.Type, dl.URL, username, password); err != nil {
		s.writeTestResult(w, false, dl.URL, err.Error(), time.Since(started))
		return
	}
	s.writeTestResult(w, true, dl.URL, "Reachable, and the credential was accepted.", time.Since(started))
}

// --- mappings ---

func (s *Server) createMapping(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	msID, err1 := strconv.ParseInt(r.FormValue("media_server_id"), 10, 64)
	dlID, err2 := strconv.ParseInt(r.FormValue("downloader_id"), 10, 64)
	if err1 != nil || err2 != nil {
		s.renderConfigError(w, r, "mappings", "Choose both a media server and a downloader.")
		return
	}
	if _, err := s.db.CreateMapping(msID, dlID); err != nil {
		slog.Error("creating mapping", "error", err)
		s.renderConfigError(w, r, "mappings", "That mapping already exists.")
		return
	}
	s.applyConfigChange(false)
	http.Redirect(w, r, "/configuration?tab=mappings", http.StatusSeeOther)
}

func (s *Server) toggleMapping(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	mappings, err := s.db.ListMappings()
	if err != nil {
		slog.Error("listing mappings", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	current := false
	for _, m := range mappings {
		if m.ID == id {
			current = m.Enabled
			break
		}
	}
	if err := s.db.SetMappingEnabled(id, !current); err != nil {
		slog.Error("toggling mapping", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(false)
	http.Redirect(w, r, "/configuration?tab=mappings", http.StatusSeeOther)
}

func (s *Server) deleteMapping(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteMapping(id); err != nil {
		slog.Error("deleting mapping", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(false)
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
	Dismissable bool
}

// writeTestResult renders a connection test into its bounded box.
//
// The backend's own error string is shown verbatim in mono, wrapped by
// a plain-language headline rather than replaced by one — the machine's
// words are usually the only thing that says what actually went wrong.
func (s *Server) writeTestResult(w http.ResponseWriter, ok bool, target, message string, elapsed time.Duration) {
	view := testResultView{
		State: "failed", Tone: "critical", Icon: "unreachable", Label: "Failed",
		Target: target, Detail: message, Dismissable: true,
	}
	if ok {
		view.State, view.Tone, view.Icon, view.Label = "connected", "good", "normal", "Connected"
	}
	if elapsed > 0 {
		view.Elapsed = fmt.Sprintf("%.1fs", elapsed.Seconds())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates["configuration"].ExecuteTemplate(w, "testresult", view); err != nil {
		slog.Error("rendering test result", "error", err)
	}
}


// saveSettings writes the runtime knobs. Each is validated by the
// settings package against its own declared bounds; an out-of-range
// value is reported rather than silently clamped on the way in.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	for _, def := range settings.Definitions {
		value := strings.TrimSpace(r.FormValue(def.Key))
		if value == "" {
			continue
		}
		if err := s.settings.Set(def.Key, value); err != nil {
			s.renderConfigError(w, r, "about", err.Error())
			return
		}
	}
	http.Redirect(w, r, "/configuration?tab=about", http.StatusSeeOther)
}
