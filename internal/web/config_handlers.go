package web

import (
	"context"
	"database/sql"
	"html/template"
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
}

type downloaderView struct {
	ID                                       int64
	Name, Type, URL                          string
	HasCredential                            bool
	DefaultDownloadLimit, DefaultUploadLimit int64
	BackupDownloadLimit, BackupUploadLimit   int64
	Enabled                                  bool
}

type mappingView struct {
	ID                              int64
	MediaServerID, DownloaderID     int64
	MediaServerName, DownloaderName string
	Enabled                         bool
}

type configPageData struct {
	pageData
	Tab               string
	MediaServers      []mediaServerView
	Downloaders       []downloaderView
	Mappings          []mappingView
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

func toDownloaderView(dl db.Downloader) downloaderView {
	return downloaderView{
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
		msViews = append(msViews, toMediaServerView(ms))
	}

	dlByID := make(map[int64]db.Downloader, len(downloaders))
	dlViews := make([]downloaderView, 0, len(downloaders))
	for _, dl := range downloaders {
		dlByID[dl.ID] = dl
		dlViews = append(dlViews, toDownloaderView(dl))
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

	return configPageData{
		pageData: pageData{
			Title:         "Configuration",
			Active:        "configuration",
			Authenticated: true,
			CSRFToken:     s.csrf.TokenFor(sessionToken),
		},
		Tab:          tab,
		MediaServers: msViews,
		Downloaders:  dlViews,
		Mappings:     mapViews,
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
					data.EditDownloader = toDownloaderView(dl)
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

func (s *Server) saveMediaServer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	msType := r.FormValue("type")
	rawURL := strings.TrimSpace(r.FormValue("url"))
	token := r.FormValue("token")
	skipLocal := r.FormValue("skip_local_playback") == "on"
	enabled := r.FormValue("enabled") == "on"

	if name == "" || rawURL == "" || msType != "plex" {
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
		_, err = s.db.CreateMediaServer(ms)
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
		writeTestResult(w, false, "bad form data")
		return
	}
	rawURL := strings.TrimSpace(r.FormValue("url"))
	token := r.FormValue("token")
	if rawURL == "" || token == "" {
		writeTestResult(w, false, "enter a URL and token first")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	if err := clients.TestPlex(ctx, rawURL, token); err != nil {
		writeTestResult(w, false, err.Error())
		return
	}
	writeTestResult(w, true, "connected successfully")
}

func (s *Server) testMediaServerByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeTestResult(w, false, "bad id")
		return
	}
	ms, err := s.db.GetMediaServer(id)
	if err != nil {
		writeTestResult(w, false, "media server not found")
		return
	}
	token, err := crypto.Decrypt(s.encryptionKey, ms.CredentialEncrypted)
	if err != nil {
		writeTestResult(w, false, "stored credential could not be decrypted")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	if err := clients.TestPlex(ctx, ms.URL, token); err != nil {
		writeTestResult(w, false, err.Error())
		return
	}
	writeTestResult(w, true, "connected successfully")
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

	if name == "" || rawURL == "" || dlType != "qbittorrent" {
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
		_, err = s.db.CreateDownloader(dl)
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
		writeTestResult(w, false, "bad form data")
		return
	}
	rawURL := strings.TrimSpace(r.FormValue("url"))
	if rawURL == "" {
		writeTestResult(w, false, "enter a URL first")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	if err := clients.TestQBittorrent(ctx, rawURL, username, password); err != nil {
		writeTestResult(w, false, err.Error())
		return
	}
	writeTestResult(w, true, "connected successfully")
}

func (s *Server) testDownloaderByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeTestResult(w, false, "bad id")
		return
	}
	dl, err := s.db.GetDownloader(id)
	if err != nil {
		writeTestResult(w, false, "downloader not found")
		return
	}

	var username, password string
	if dl.CredentialEncrypted.Valid && dl.CredentialEncrypted.String != "" {
		raw, err := crypto.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
		if err != nil {
			writeTestResult(w, false, "stored credential could not be decrypted")
			return
		}
		cred, err := credential.DecodeDownloader(raw)
		if err != nil {
			writeTestResult(w, false, "stored credential is corrupt")
			return
		}
		username, password = cred.Username, cred.Password
	}

	ctx, cancel := context.WithTimeout(r.Context(), testConnectionTimeout)
	defer cancel()
	if err := clients.TestQBittorrent(ctx, dl.URL, username, password); err != nil {
		writeTestResult(w, false, err.Error())
		return
	}
	writeTestResult(w, true, "connected successfully")
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

func writeTestResult(w http.ResponseWriter, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	cls, label := "alert-error", "Failed"
	if ok {
		cls, label = "alert-success", "Connected"
	}
	_, _ = w.Write([]byte(`<div class="alert ` + cls + ` text-xs py-1.5 px-2 mt-2">` + label + `: ` + template.HTMLEscapeString(message) + `</div>`))
}
