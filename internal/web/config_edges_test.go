package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// rawPost sends a body as typed, the CSRF token in its header so a body
// the form parser rejects still reaches the handler.
func (a *testApp) rawPost(path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", a.csrf)
	for _, c := range a.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

// Every dot a Connections row can show, from the state behind it.
func TestEveryConnectionDot(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("Jelly", "jellyfin", "http://jf.lan")
	_, _ = app.db.CreateMediaServer(db.MediaServer{Name: "Off", Type: "emby", URL: "http://emby.lan"})
	app.watchers.running[ms] = true
	dl := app.downloader("qBit")
	_, _ = app.db.CreateDownloader(db.Downloader{Name: "Idle", Type: "transmission", URL: "http://tr.lan"})
	app.setMapping(ms, dl, true)
	waitFor(t, "the downloader to resolve", func() bool { return app.sched.State(dl).Known })

	page := app.get("/configuration").Body.String()
	for _, want := range []string{"No activity detected", "Not watched for playback", "Excluded from throttling", "Nothing mapped is playing"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q dot", want)
		}
	}

	app.sched.HandleSessionEvent(ms, clients.Session{Key: "s"}, true)
	waitFor(t, "the throttle", func() bool { return app.sched.State(dl).Throttled })
	page = app.get("/configuration").Body.String()
	for _, want := range []string{"1 active session", "Jelly is playing"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q dot", want)
		}
	}
}

func TestAuthSummary(t *testing.T) {
	disabled := newAppWith(t, map[string]string{"AUTH_DISABLED": "true"})
	expectBody(t, disabled.get("/configuration?tab=about"), "anyone who can reach this app is signed in")

	app := newAppWith(t, nil)
	app.post("/setup", app.setupForm("owner", "a-long-enough-password"))
	rec := app.get("/configuration?tab=about")
	expectBody(t, rec, "signed in as owner")
	if strings.Contains(rec.Body.String(), "from the environment") {
		t.Error("a created account is described as coming from the environment")
	}
}

// Links to an entry that no longer exists open the page without a drawer.
func TestSheetsForMissingEntries(t *testing.T) {
	app := newApp(t)
	for _, path := range []string{"/configuration?tab=media-servers&edit=99", "/configuration?tab=downloaders&edit=99"} {
		rec := app.get(path)
		expectStatus(t, rec, http.StatusOK)
		if strings.Contains(rec.Body.String(), `name="id" value="99"`) {
			t.Errorf("%s: a drawer for a missing entry", path)
		}
	}
	expectStatus(t, app.get("/configuration/downloaders/99/edit"), http.StatusNotFound)
	expectStatus(t, app.get("/configuration/media-servers/99/edit"), http.StatusNotFound)
	expectStatus(t, app.get("/configuration/media-servers/x/edit"), http.StatusBadRequest)

	dl := app.downloader("qBit")
	expectBody(t, app.get(fmt.Sprintf("/configuration/downloaders/%d/edit", dl)), `value="qBit"`)
}

func TestMalformedRequestsAreRefused(t *testing.T) {
	app := newApp(t)
	app.mediaServer("Jelly", "jellyfin", "http://jf.lan")

	for _, path := range []string{
		"/configuration/media-servers", "/configuration/downloaders", "/configuration/mappings/set",
	} {
		expectStatus(t, app.rawPost(path, "%zz"), http.StatusBadRequest)
	}
	for _, path := range []string{"/configuration/media-servers/test", "/configuration/downloaders/test"} {
		expectBody(t, app.rawPost(path, "%zz"), "bad form data")
	}

	server := mediaServerForm("Jelly 2", "jellyfin", "http://jf2.lan", "key")
	server.Set("id", "x")
	expectStatus(t, app.post("/configuration/media-servers", server), http.StatusBadRequest)
	server.Set("id", "99")
	expectStatus(t, app.post("/configuration/media-servers", server), http.StatusInternalServerError)

	downloader := url.Values{"name": {"Other"}, "type": {"qbittorrent"}, "url": {"http://other.lan"}, "id": {"x"}}
	expectStatus(t, app.post("/configuration/downloaders", downloader), http.StatusBadRequest)
	downloader.Set("id", "99")
	expectStatus(t, app.post("/configuration/downloaders", downloader), http.StatusInternalServerError)
	downloader.Del("id")
	downloader.Set("type", "rtorrent")
	expectStatus(t, app.post("/configuration/downloaders", downloader), http.StatusUnprocessableEntity)

	for _, path := range []string{"/configuration/media-servers/x/delete", "/configuration/downloaders/x/delete"} {
		expectStatus(t, app.post(path, nil), http.StatusBadRequest)
	}
	for path, want := range map[string]string{
		"/configuration/media-servers/x/test":  "bad id",
		"/configuration/media-servers/99/test": "media server not found",
		"/configuration/downloaders/x/test":    "bad id",
		"/configuration/downloaders/99/test":   "downloader not found",
	} {
		expectBody(t, app.post(path, nil), want)
	}
}

// A blank Plex form, the pasted token beats the sign-in's, and one with
// neither is refused.
func TestMediaServerCredentialFields(t *testing.T) {
	app := newApp(t)
	form := url.Values{"name": {"Plex"}, "type": {"plex"}, "url": {"http://plex.lan:32400"}, "enabled": {"on"},
		"token": {"pasted"}, "plex_token": {"signed-in"}}
	expectRedirect(t, app.post("/configuration/media-servers", form), "/configuration?tab=connections")
	servers, _ := app.db.ListMediaServers()
	if token := app.decrypt(servers[0].CredentialEncrypted); token != "pasted" {
		t.Errorf("saved %q, want the pasted token", token)
	}

	form = url.Values{"name": {"Plex 2"}, "type": {"plex"}, "url": {"http://plex2.lan:32400"}, "plex_token": {"signed-in"}}
	expectRedirect(t, app.post("/configuration/media-servers", form), "/configuration?tab=connections")
	servers, _ = app.db.ListMediaServers()
	if token := app.decrypt(servers[1].CredentialEncrypted); token != "signed-in" {
		t.Errorf("saved %q, want the sign-in token", token)
	}

	form = url.Values{"name": {"Plex 3"}, "type": {"plex"}, "url": {"http://plex3.lan:32400"}}
	expectBody(t, app.post("/configuration/media-servers", form), "A token is required.")
}

// A stored credential that no longer decrypts (the key file was
// replaced) asks for it again rather than sending garbage.
func TestUndecryptableCredentials(t *testing.T) {
	app := newApp(t)
	ms, _ := app.db.CreateMediaServer(db.MediaServer{Name: "Jelly", Type: "jellyfin", URL: "http://jf.lan", CredentialEncrypted: "garbled"})
	dl, _ := app.db.CreateDownloader(db.Downloader{Name: "qBit", Type: "qbittorrent", URL: app.qbit.URL,
		CredentialEncrypted: sql.NullString{String: "garbled", Valid: true}})

	expectBody(t, app.post(fmt.Sprintf("/configuration/media-servers/%d/test", ms), nil), "could not be decrypted")
	expectBody(t, app.post(fmt.Sprintf("/configuration/downloaders/%d/test", dl), nil), "could not be read")

	// The form's test, with a blank credential, has nothing to send.
	form := mediaServerForm("Jelly", "jellyfin", "http://jf.lan", "")
	form.Set("id", fmt.Sprint(ms))
	expectBody(t, app.post("/configuration/media-servers/test", form), "Nothing to test")
	form.Set("id", "x")
	expectBody(t, app.post("/configuration/media-servers/test", form), "Nothing to test")

	// Still listed, with the username left blank rather than failing the page.
	expectBody(t, app.get("/configuration"), "qBit")
}

func TestConnectionTestsNeedASupportedType(t *testing.T) {
	app := newApp(t)
	server := mediaServerForm("X", "kodi", "http://x.lan", "key")
	expectBody(t, app.post("/configuration/media-servers/test", server), "choose a supported media server type first")
	downloader := url.Values{"name": {"X"}, "type": {"rtorrent"}, "url": {"http://x.lan"}}
	expectBody(t, app.post("/configuration/downloaders/test", downloader), "choose a supported downloader type first")
	downloader.Del("url")
	expectBody(t, app.post("/configuration/downloaders/test", downloader), "Nothing to test")

	downloader = url.Values{"name": {"X"}, "type": {"qbittorrent"}, "url": {"http://127.0.0.1:1"}}
	expectBody(t, app.post("/configuration/downloaders/test", downloader), "Failed")
}

func TestDownloaderCredentialChoices(t *testing.T) {
	app := newApp(t)
	form := url.Values{"name": {"qBit"}, "type": {"qbittorrent"}, "url": {app.qbit.URL}, "password": {"secret"}}
	expectBody(t, app.post("/configuration/downloaders", form), "Enter the username that goes with the password.")

	form.Set("username", "admin")
	expectRedirect(t, app.post("/configuration/downloaders", form), "/configuration?tab=connections")
	dls, _ := app.db.ListDownloaders()

	form = url.Values{"id": {fmt.Sprint(dls[0].ID)}, "name": {"qBit"}, "type": {"qbittorrent"}, "url": {app.qbit.URL},
		"clear_credentials": {"on"}, "default_download_limit": {"-5"}, "default_upload_limit": {"lots"}}
	expectRedirect(t, app.post("/configuration/downloaders", form), "/configuration?tab=connections")
	got, _ := app.db.GetDownloader(dls[0].ID)
	if got.CredentialEncrypted.Valid || got.DefaultDownloadLimit != 0 || got.DefaultUploadLimit != 0 {
		t.Errorf("after clearing: %+v", got)
	}

	expectRedirect(t, app.post(fmt.Sprintf("/configuration/downloaders/%d/delete", got.ID), nil), "/configuration?tab=connections")
	if dls, _ := app.db.ListDownloaders(); len(dls) != 0 {
		t.Errorf("not deleted: %+v", dls)
	}
}

// A mapping toggled from the matrix re-renders the matrix, ticked.
func TestMappingToggleFromTheMatrix(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("Jelly", "jellyfin", "http://jf.lan")
	dl := app.downloader("qBit")
	app.htmx = true
	rec := app.post("/configuration/mappings/set", url.Values{"media_server_id": {fmt.Sprint(ms)}, "downloader_id": {fmt.Sprint(dl)}, "enabled": {"1"}})
	expectStatus(t, rec, http.StatusOK)
	expectBody(t, rec, `aria-pressed="true"`)
}

// With the database gone, every page and save fails plainly. Sign-in is
// off, or the session check would fail first.
func TestConfigurationOnAFailedDatabase(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_DISABLED": "true"})
	ms := app.mediaServer("Jelly", "jellyfin", "http://jf.lan")
	dl := app.downloader("qBit")
	_ = app.db.Close()

	for _, path := range []string{"/configuration", "/configuration/connections/status"} {
		expectStatus(t, app.get(path), http.StatusInternalServerError)
	}
	for path, form := range map[string]url.Values{
		"/configuration/media-servers":                            {"name": {"x"}},
		fmt.Sprintf("/configuration/media-servers/%d/delete", ms): nil,
		fmt.Sprintf("/configuration/downloaders/%d/delete", dl):   nil,
		"/configuration/mappings/set":                             {"media_server_id": {fmt.Sprint(ms)}, "downloader_id": {fmt.Sprint(dl)}},
	} {
		expectStatus(t, app.post(path, form), http.StatusInternalServerError)
	}
	expectStatus(t, app.post("/configuration/media-servers", mediaServerForm("New", "jellyfin", "http://new.lan", "k")), http.StatusInternalServerError)
	expectStatus(t, app.post("/configuration/downloaders", url.Values{"name": {"New"}, "type": {"qbittorrent"}, "url": {"http://new.lan"}}), http.StatusInternalServerError)
}

func (a *testApp) decrypt(sealed string) string {
	a.t.Helper()
	token, err := crypto.Decrypt(a.srv.encryptionKey, sealed)
	if err != nil {
		a.t.Fatal(err)
	}
	return token
}
