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
	expectRedirect(t, rec, "/configuration?tab=media-servers")

	servers, _ := app.db.ListMediaServers()
	if len(servers) != 1 || servers[0].Name != "Living Room" || !servers[0].Enabled {
		t.Fatalf("saved servers = %+v", servers)
	}
	// The key is encrypted at rest, never stored as typed.
	if servers[0].CredentialEncrypted == "" || strings.Contains(servers[0].CredentialEncrypted, "secret-key") {
		t.Errorf("credential stored as %q", servers[0].CredentialEncrypted)
	}
	// Saving reconciles the watchers, so the change is live without a restart.
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

func TestEditingKeepsTheStoredCredential(t *testing.T) {
	app := newApp(t)
	app.post("/configuration/media-servers", mediaServerForm("JF", "jellyfin", "http://jf.lan", "original-key"))
	servers, _ := app.db.ListMediaServers()
	before := servers[0].CredentialEncrypted

	// Edited with its own name and address — not a clash with itself —
	// and the key left blank, which means "keep what's saved".
	form := mediaServerForm("JF renamed", "jellyfin", "http://jf.lan", "")
	form.Set("id", fmt.Sprint(servers[0].ID))
	expectRedirect(t, app.post("/configuration/media-servers", form), "/configuration?tab=media-servers")

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

	expectRedirect(t, app.post(fmt.Sprintf("/configuration/media-servers/%d/delete", ms), nil), "/configuration?tab=media-servers")
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
	expectRedirect(t, app.post("/configuration/downloaders", form), "/configuration?tab=downloaders")

	dls, _ := app.db.ListDownloaders()
	if len(dls) != 1 || dls[0].BackupDownloadLimit != 1500 || dls[0].BackupUploadLimit != 300 {
		t.Fatalf("saved downloaders = %+v", dls)
	}
	if !dls[0].CredentialEncrypted.Valid || strings.Contains(dls[0].CredentialEncrypted.String, "hunter2") {
		t.Errorf("credential stored as %+v", dls[0].CredentialEncrypted)
	}

	// Half a credential is refused rather than half-saved.
	half := url.Values{"name": {"Other"}, "type": {"qbittorrent"}, "url": {"http://other.lan"}, "username": {"admin"}}
	expectStatus(t, app.post("/configuration/downloaders", half), http.StatusUnprocessableEntity)

	dup := url.Values{"name": {"Second"}, "type": {"qbittorrent"}, "url": {app.qbit.URL + "/"}}
	rec := app.post("/configuration/downloaders", dup)
	expectStatus(t, rec, http.StatusUnprocessableEntity)
	expectBody(t, rec, "is already added as")
}

// setMapping ticks or unticks one cell of the mappings matrix.
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

	// Ticking the same cell twice is one mapping, not two.
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

// fakeJellyfin answers /System/Info only when the right key arrives, the
// way a real server does — so the test exercises the request the client
// actually builds, not a stub of it.
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

// A new server or downloader must start switched on: unticked by
// default, one saved without noticing was silently never used.
func TestNewEntriesStartEnabled(t *testing.T) {
	app := newApp(t)
	for _, tab := range []string{"media-servers", "downloaders"} {
		body := app.get("/configuration?tab=" + tab).Body.String()
		if !strings.Contains(body, `name="enabled" checked`) {
			t.Errorf("%s: the Enabled box starts unticked for a new entry", tab)
		}
	}
	// Editing still shows the entry's real state.
	id := app.mediaServer("Off", "jellyfin", "http://off.lan")
	ms, _ := app.db.GetMediaServer(id)
	ms.Enabled = false
	_ = app.db.UpdateMediaServer(ms)
	body := app.get(fmt.Sprintf("/configuration?tab=media-servers&edit=%d", id)).Body.String()
	if strings.Contains(body, `name="enabled" checked`) {
		t.Error("editing a disabled server shows it as enabled")
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
		{"no credentials says none were checked", form(app.qbit.URL, "", ""), "No credentials were sent"},
		{"credentials accepted", form(app.qbit.URL, "admin", "pw"), "Successfully connected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := app.post("/configuration/downloaders/test", c.form)
			expectStatus(t, rec, http.StatusOK)
			expectBody(t, rec, c.want)
		})
	}
}

// The Test button on a saved row uses what is stored.
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
