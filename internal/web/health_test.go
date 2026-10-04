package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// Each backend must be probed as what it is: a Jellyfin server or a
// Transmission daemon checked as Plex or qBittorrent never passes.
func TestHealthProbesEachBackendByType(t *testing.T) {
	app := newApp(t)
	jf := fakeJellyfin(t)
	expectRedirect(t, app.post("/configuration/media-servers", mediaServerForm("JF", "jellyfin", jf.URL, "good-key")), "/configuration?tab=media-servers")
	servers, _ := app.db.ListMediaServers()
	app.watchers.running[servers[0].ID] = true

	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	t.Cleanup(tr.Close)
	if _, err := app.db.CreateDownloader(db.Downloader{Name: "Tr", Type: "transmission", URL: tr.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	rec := app.get("/health")
	var got healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || got.Status != "ok" {
		t.Fatalf("health = %d %+v", rec.Code, got)
	}

	// And a backend that is really down still degrades it.
	app.mediaServer("Gone", "emby", "http://127.0.0.1:1")
	if rec := app.get("/health"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an unreachable server left health at %d", rec.Code)
	}
}
