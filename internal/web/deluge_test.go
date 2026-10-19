package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// fakeDelugeWebUI is a Deluge Web UI already connected to its daemon,
// with the password "deluge-pass".
func fakeDelugeWebUI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "auth.login":
			var pw string
			_ = json.Unmarshal(req.Params[0], &pw)
			result = pw == "deluge-pass"
		case "web.connected":
			result = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": 1})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func delugeForm(rawURL, password string) url.Values {
	return url.Values{"name": {"Deluge"}, "type": {"deluge"}, "url": {rawURL}, "enabled": {"on"}, "password": {password}}
}

// Deluge's Web UI has a password and no username.
func TestDelugeSavesWithAPasswordAlone(t *testing.T) {
	app := newApp(t)
	deluge := fakeDelugeWebUI(t)
	form := delugeForm(deluge.URL, "deluge-pass")
	// A username left over from switching the type is not kept.
	form.Set("username", "admin")
	expectRedirect(t, app.post("/configuration/downloaders", form), "/configuration?tab=connections")

	dls, _ := app.db.ListDownloaders()
	if len(dls) != 1 || dls[0].Type != "deluge" {
		t.Fatalf("downloaders = %+v", dls)
	}
	user, pass, err := credential.Decrypt(app.srv.encryptionKey, dls[0].CredentialEncrypted.String)
	if err != nil || user != "" || pass != "deluge-pass" {
		t.Errorf("stored credential = %q/%q, %v; want no username and the password", user, pass, err)
	}

	// An edit without the password keeps the saved one.
	edit := delugeForm(deluge.URL, "")
	edit.Set("id", fmt.Sprint(dls[0].ID))
	edit.Set("name", "Deluge renamed")
	expectRedirect(t, app.post("/configuration/downloaders", edit), "/configuration?tab=connections")
	if after, _ := app.db.GetDownloader(dls[0].ID); after.Name != "Deluge renamed" || after.CredentialEncrypted != dls[0].CredentialEncrypted {
		t.Errorf("editing without the password: %+v", after)
	}
}

func TestDelugeConnectionTest(t *testing.T) {
	app := newApp(t)
	deluge := fakeDelugeWebUI(t)

	expectBody(t, app.post("/configuration/downloaders/test", delugeForm(deluge.URL, "deluge-pass")), "Successfully connected.")

	rec := app.post("/configuration/downloaders/test", delugeForm(deluge.URL, "wrong"))
	expectBody(t, rec, "the password was rejected")
	if strings.Contains(rec.Body.String(), "username") {
		t.Error("the hint mentions a username Deluge doesn't have")
	}
}

func TestDownloaderFormOffersDeluge(t *testing.T) {
	app := newApp(t)
	rec := app.get("/configuration/downloaders/new")
	expectBody(t, rec, `<option value="deluge"`)
	expectBody(t, rec, `data-types="qbittorrent transmission"`)
}

// The address example is each type's usual port, rendered for the saved
// type and carried by every option for when the type changes.
func TestAddressExampleFollowsTheType(t *testing.T) {
	app := newApp(t)
	rec := app.get("/configuration/downloaders/new")
	expectBody(t, rec, `placeholder="http://192.168.1.20:8080"`)
	for _, port := range []string{"8080", "9091", "8112"} {
		expectBody(t, rec, `data-placeholder="http://192.168.1.20:`+port+`"`)
	}

	id, err := app.db.CreateDownloader(db.Downloader{Name: "Transmission", Type: "transmission", URL: "http://tr.lan:9091", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	expectBody(t, app.get(fmt.Sprintf("/configuration/downloaders/%d/edit", id)), `placeholder="http://192.168.1.20:9091"`)

	jf := app.mediaServer("Jellyfin", "jellyfin", "http://jf.lan:8096")
	expectBody(t, app.get(fmt.Sprintf("/configuration/media-servers/%d/edit", jf)), `placeholder="http://192.168.1.50:8096"`)
	expectBody(t, app.get("/configuration/media-servers/new"), `placeholder="http://192.168.1.50:32400"`)
}
