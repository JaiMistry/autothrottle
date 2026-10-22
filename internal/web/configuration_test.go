package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func mediaServerForm(name, msType, rawURL, key string) url.Values {
	return url.Values{"name": {name}, "type": {msType}, "url": {rawURL}, "api_token": {key}, "enabled": {"on"}}
}

func TestAddMediaServer(t *testing.T) {
	app := newApp(t)
	rec := app.post("/configuration/media-servers", mediaServerForm("Living Room", "jellyfin", "http://jf.lan:8096", "secret-key"))
	expectRedirect(t, rec, "/configuration?tab=connections")

	servers, _ := app.db.ListMediaServers()
	if len(servers) != 1 || servers[0].Name != "Living Room" || !servers[0].Enabled {
		t.Fatalf("saved servers = %+v", servers)
	}
	if servers[0].CredentialEncrypted == "" || strings.Contains(servers[0].CredentialEncrypted, "secret-key") {
		t.Errorf("credential stored as %q", servers[0].CredentialEncrypted)
	}
	// Live without a restart.
	if app.watchers.reconciles == 0 {
		t.Error("saving did not reconcile the watchers")
	}
}

func TestAddMediaServerValidation(t *testing.T) {
	app := newApp(t)
	cases := []struct {
		name string
		form url.Values
	}{
		{"no name", mediaServerForm("", "jellyfin", "http://jf.lan", "k")},
		{"no address", mediaServerForm("JF", "jellyfin", "", "k")},
		{"unknown type", mediaServerForm("JF", "kodi", "http://jf.lan", "k")},
	}
	for _, c := range cases {
		rec := app.post("/configuration/media-servers", c.form)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", c.name, rec.Code)
		}
	}
	if servers, _ := app.db.ListMediaServers(); len(servers) != 0 {
		t.Errorf("an invalid form saved %d servers", len(servers))
	}
}

func TestDuplicateMediaServersAreRefused(t *testing.T) {
	app := newApp(t)
	app.mediaServer("Living Room", "jellyfin", "http://jf.lan:8096")

	rec := app.post("/configuration/media-servers", mediaServerForm("Bedroom", "jellyfin", "HTTP://JF.LAN:8096/", "k"))
	expectStatus(t, rec, http.StatusUnprocessableEntity)
	expectBody(t, rec, "is already added as")

	rec = app.post("/configuration/media-servers", mediaServerForm(" living room ", "emby", "http://emby.lan", "k"))
	expectStatus(t, rec, http.StatusUnprocessableEntity)
	expectBody(t, rec, "already exists")

	if servers, _ := app.db.ListMediaServers(); len(servers) != 1 {
		t.Errorf("%d servers after two refused duplicates", len(servers))
	}
}

// fakeIdentity is a media server that answers only with its own ID, as
// Plex and Emby/Jellyfin each report it.
func fakeIdentity(t *testing.T, id string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity":
			fmt.Fprintf(w, `{"MediaContainer":{"machineIdentifier":%q}}`, id)
		case "/System/Info/Public":
			fmt.Fprintf(w, `{"Id":%q}`, id)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// One server under two addresses is refused by the ID it reports; two
// servers are not, even behind one address, as a reverse proxy puts them.
func TestOneServerUnderTwoAddressesIsRefused(t *testing.T) {
	for _, msType := range []string{"plex", "jellyfin", "emby"} {
		app := newApp(t)
		app.mediaServer("Main", msType, fakeIdentity(t, "machine-1"))

		rec := app.post("/configuration/media-servers", mediaServerForm("Again", msType, fakeIdentity(t, "machine-1"), "k"))
		expectStatus(t, rec, http.StatusUnprocessableEntity)
		expectBody(t, rec, "same server as &#34;Main&#34;")

		other := mediaServerForm("Other", msType, fakeIdentity(t, "machine-2"), "k")
		other.Set("token", "k") // Plex's field
		expectRedirect(t, app.post("/configuration/media-servers", other), "/configuration?tab=connections")
	}

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"MediaContainer":{"machineIdentifier":%q}}`, r.Host[:strings.Index(r.Host, ":")])
	}))
	t.Cleanup(proxy.Close)
	app := newApp(t)
	app.mediaServer("Plex", "plex", proxy.URL)
	form := mediaServerForm("Plex 2", "plex", strings.Replace(proxy.URL, "127.0.0.1", "localhost", 1), "k")
	form.Set("token", "k")
	expectRedirect(t, app.post("/configuration/media-servers", form), "/configuration?tab=connections")
}

func TestEditingKeepsTheStoredCredential(t *testing.T) {
	app := newApp(t)
	app.post("/configuration/media-servers", mediaServerForm("JF", "jellyfin", "http://jf.lan", "original-key"))
	servers, _ := app.db.ListMediaServers()
	before := servers[0].CredentialEncrypted

	// Its own name and address are not a clash; a blank key keeps the saved one.
	form := mediaServerForm("JF renamed", "jellyfin", "http://jf.lan", "")
	form.Set("id", fmt.Sprint(servers[0].ID))
	expectRedirect(t, app.post("/configuration/media-servers", form), "/configuration?tab=connections")

	after, _ := app.db.GetMediaServer(servers[0].ID)
	if after.Name != "JF renamed" {
		t.Errorf("name = %q", after.Name)
	}
	if after.CredentialEncrypted != before {
		t.Error("a blank key on edit replaced the stored one")
	}
}

func TestDeletingAServerRemovesItsMappings(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("JF", "jellyfin", "http://jf.lan")
	dl := app.downloader("qBit")
	app.setMapping(ms, dl, true)

	expectRedirect(t, app.post(fmt.Sprintf("/configuration/media-servers/%d/delete", ms), nil), "/configuration?tab=connections")
	if mappings, _ := app.db.ListMappings(); len(mappings) != 0 {
		t.Errorf("%d mappings outlived their server", len(mappings))
	}
}

func TestAddDownloader(t *testing.T) {
	app := newApp(t)
	form := url.Values{
		"name": {"qBit"}, "type": {"qbittorrent"}, "url": {app.qbit.URL}, "enabled": {"on"},
		"username": {"admin"}, "password": {"hunter2"},
		"default_download_limit": {"0"}, "default_upload_limit": {"0"},
		"backup_download_limit": {"1500"}, "backup_upload_limit": {"300"},
	}
	expectRedirect(t, app.post("/configuration/downloaders", form), "/configuration?tab=connections")

	dls, _ := app.db.ListDownloaders()
	if len(dls) != 1 || dls[0].BackupDownloadLimit != 1500 || dls[0].BackupUploadLimit != 300 {
		t.Fatalf("saved downloaders = %+v", dls)
	}
	if !dls[0].CredentialEncrypted.Valid || strings.Contains(dls[0].CredentialEncrypted.String, "hunter2") {
		t.Errorf("credential stored as %+v", dls[0].CredentialEncrypted)
	}

	half := url.Values{"name": {"Other"}, "type": {"qbittorrent"}, "url": {"http://other.lan"}, "username": {"admin"}}
	expectStatus(t, app.post("/configuration/downloaders", half), http.StatusUnprocessableEntity)

	dup := url.Values{"name": {"Second"}, "type": {"qbittorrent"}, "url": {app.qbit.URL + "/"}}
	rec := app.post("/configuration/downloaders", dup)
	expectStatus(t, rec, http.StatusUnprocessableEntity)
	expectBody(t, rec, "is already added as")

	dup = url.Values{"name": {"QBIT "}, "type": {"qbittorrent"}, "url": {"http://elsewhere.lan"}}
	expectBody(t, app.post("/configuration/downloaders", dup), "already exists")
}

func (a *testApp) setMapping(ms, dl int64, on bool) {
	a.t.Helper()
	form := url.Values{"media_server_id": {fmt.Sprint(ms)}, "downloader_id": {fmt.Sprint(dl)}}
	if on {
		form.Set("enabled", "on")
	}
	expectRedirect(a.t, a.post("/configuration/mappings/set", form), "/configuration?tab=mappings")
}

func TestMappings(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("JF", "jellyfin", "http://jf.lan")
	dl := app.downloader("qBit")

	app.setMapping(ms, dl, true)
	app.setMapping(ms, dl, true)
	mappings, _ := app.db.ListMappings()
	if len(mappings) != 1 || !mappings[0].Enabled {
		t.Fatalf("mappings = %+v", mappings)
	}
	app.setMapping(ms, dl, false)
	if mappings, _ = app.db.ListMappings(); mappings[0].Enabled {
		t.Error("unticking left the mapping enabled")
	}

	expectStatus(t, app.post("/configuration/mappings/set", url.Values{"media_server_id": {"x"}}), http.StatusBadRequest)
}

// fakeJellyfin answers /System/Info only for the right key, so tests
// exercise the request the client really builds.
func fakeJellyfin(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info" {
			http.NotFound(w, r)
			return
		}
		for _, vals := range r.Header {
			for _, v := range vals {
				if strings.Contains(v, "good-key") {
					_, _ = w.Write([]byte(`{"ServerName":"fake"}`))
					return
				}
			}
		}
		if strings.Contains(r.URL.RawQuery, "good-key") {
			_, _ = w.Write([]byte(`{"ServerName":"fake"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConnectionTest(t *testing.T) {
	app := newApp(t)
	jf := fakeJellyfin(t)

	cases := []struct {
		name, url, key, wantState, wantText string
	}{
		{"credential accepted", jf.URL, "good-key", `data-state="connected"`, "Successfully connected"},
		{"credential rejected", jf.URL, "bad-key", `data-state="failed"`, "API key was rejected"},
		{"nothing listening", "http://127.0.0.1:1", "good-key", `data-state="failed"`, "Nothing is listening"},
		{"fields missing", "", "", `data-state="failed"`, "Nothing to test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := app.post("/configuration/media-servers/test", mediaServerForm("JF", "jellyfin", c.url, c.key))
			expectStatus(t, rec, http.StatusOK)
			expectBody(t, rec, c.wantState)
			if c.wantText != "" {
				expectBody(t, rec, c.wantText)
			}
		})
	}
}

// A new entry starts enabled; unticked, one saved unnoticed never ran.
func TestNewEntriesStartEnabled(t *testing.T) {
	app := newApp(t)
	for _, path := range []string{"/configuration/media-servers/new", "/configuration/downloaders/new"} {
		body := app.get(path).Body.String()
		if !strings.Contains(body, `name="enabled" checked`) {
			t.Errorf("%s: the Enabled box starts unticked for a new entry", path)
		}
	}
	id := app.mediaServer("Off", "jellyfin", "http://off.lan")
	ms, _ := app.db.GetMediaServer(id)
	ms.Enabled = false
	_ = app.db.UpdateMediaServer(ms)
	body := app.get(fmt.Sprintf("/configuration/media-servers/%d/edit", id)).Body.String()
	if strings.Contains(body, `name="enabled" checked`) {
		t.Error("editing a disabled server shows it as enabled")
	}
	if !strings.Contains(body, fmt.Sprintf(`id="del-ms-%d"`, id)) {
		t.Error("the edit drawer has no delete confirmation")
	}
	expectStatus(t, app.get("/configuration/media-servers/999/edit"), http.StatusNotFound)
}

// The old media-servers and downloaders tab links, edit links included,
// land on Connections.
func TestOldTabLinksLandOnConnections(t *testing.T) {
	app := newApp(t)
	id := app.downloader("qBit")
	body := app.get("/configuration?tab=media-servers").Body.String()
	if !strings.Contains(body, `href="/configuration?tab=connections">Connections`) || !strings.Contains(body, `aria-selected="true" href="/configuration?tab=connections"`) {
		t.Error("the old media servers tab did not land on Connections")
	}
	if strings.Contains(body, `class="at-sheet open"`) {
		t.Error("a plain tab link opened the drawer")
	}
	body = app.get(fmt.Sprintf("/configuration?tab=downloaders&edit=%d", id)).Body.String()
	if !strings.Contains(body, `class="at-sheet open"`) || !strings.Contains(body, `id="dl-form"`) {
		t.Error("an old edit link did not open the downloader in the drawer")
	}
}

// From the drawer, a save swaps the tables out of band and closes it; a
// refusal lands above the form.
func TestDrawerSave(t *testing.T) {
	app := newApp(t)
	app.htmx = true
	rec := app.post("/configuration/media-servers", mediaServerForm("Living Room", "jellyfin", "http://jf.lan:8096", "k"))
	expectStatus(t, rec, http.StatusOK)
	if rec.Header().Get("HX-Trigger") != "at-sheet-close" || rec.Header().Get("HX-Reswap") != "none" {
		t.Errorf("headers = %v", rec.Header())
	}
	expectBody(t, rec, `id="connections" class="at-connections" hx-swap-oob="true"`)
	expectBody(t, rec, "Living Room")

	rec = app.post("/configuration/media-servers", mediaServerForm("Living Room", "emby", "http://emby.lan", "k"))
	expectStatus(t, rec, http.StatusOK)
	if rec.Header().Get("HX-Retarget") != "#sheet-error" {
		t.Errorf("a refused save went to %q", rec.Header().Get("HX-Retarget"))
	}
	expectBody(t, rec, "already exists")
}

func TestConnectionStatusDots(t *testing.T) {
	app := newApp(t)
	off := app.mediaServer("Off", "jellyfin", "http://off.lan")
	ms, _ := app.db.GetMediaServer(off)
	ms.Enabled = false
	_ = app.db.UpdateMediaServer(ms)
	app.downloader("qBit")
	body := app.get("/configuration?tab=connections").Body.String()
	for _, want := range []string{
		`aria-label="Disabled. Not watched for playback"`,
		`aria-label="Unknown. State not resolved yet"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestDownloaderConnectionTest(t *testing.T) {
	app := newApp(t)
	form := func(rawURL, user, pass string) url.Values {
		return url.Values{"name": {"qBit"}, "type": {"qbittorrent"}, "url": {rawURL}, "username": {user}, "password": {pass}}
	}
	cases := []struct {
		name string
		form url.Values
		want string
	}{
		{"nothing listening names the WebUI", form("http://127.0.0.1:1", "", ""), "WebUI port"},
		{"credentials are optional", form(app.qbit.URL, "", ""), "Successfully connected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := app.post("/configuration/downloaders/test", c.form)
			expectStatus(t, rec, http.StatusOK)
			expectBody(t, rec, c.want)
		})
	}
}

// A saved row's Test uses what is stored.
func TestSavedEntryConnectionTest(t *testing.T) {
	app := newApp(t)
	jf := fakeJellyfin(t)
	app.post("/configuration/media-servers", mediaServerForm("JF", "jellyfin", jf.URL, "good-key"))
	servers, _ := app.db.ListMediaServers()
	dl := app.downloader("qBit")

	rec := app.post(fmt.Sprintf("/configuration/media-servers/%d/test", servers[0].ID), nil)
	expectBody(t, rec, `data-state="connected"`)
	rec = app.post(fmt.Sprintf("/configuration/downloaders/%d/test", dl), nil)
	expectBody(t, rec, `data-state="connected"`)
}
