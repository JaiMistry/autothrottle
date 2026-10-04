package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/db"
)

func (a *testApp) health() (int, healthResponse) {
	a.t.Helper()
	rec := a.get("/health")
	var got healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		a.t.Fatal(err)
	}
	return rec.Code, got
}

// Each backend must be probed as what it is: a Jellyfin server or a
// Transmission daemon checked as Plex or qBittorrent never passes.
func TestHealthProbesEachBackendByType(t *testing.T) {
	app := newApp(t)
	jf := fakeJellyfin(t)
	expectRedirect(t, app.post("/configuration/media-servers", mediaServerForm("JF", "jellyfin", jf.URL, "good-key")), "/configuration?tab=media-servers")
	servers, _ := app.db.ListMediaServers()
	app.watchers.running[servers[0].ID] = true

	// Answers Transmission's RPC and nothing else.
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transmission/rpc" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	t.Cleanup(tr.Close)
	if _, err := app.db.CreateDownloader(db.Downloader{Name: "Tr", Type: "transmission", URL: tr.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	app.downloader("qBit")

	if code, got := app.health(); code != http.StatusOK || got.Status != "ok" || len(got.Downloaders) != 2 {
		t.Fatalf("health = %d %+v", code, got)
	}

	// A watched server that is really down degrades it, and says why.
	gone := app.mediaServer("Gone", "emby", "http://127.0.0.1:1")
	app.watchers.running[gone] = true
	code, got := app.health()
	if code != http.StatusServiceUnavailable {
		t.Errorf("an unreachable server left health at %d", code)
	}
	for _, ms := range got.MediaServers {
		if ms.Name == "Gone" && (ms.Reachable || ms.Error == "") {
			t.Errorf("the unreachable server reads %+v", ms)
		}
	}
}

func TestHealthNeedsNoLogin(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	if code, got := app.health(); code != http.StatusOK || got.Status != "ok" {
		t.Fatalf("/health without a session or backends: %d %+v", code, got)
	}
}
