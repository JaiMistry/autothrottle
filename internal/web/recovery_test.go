package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// A qBittorrent that is down until told otherwise, as one restarting
// for an update is.
func restartingQbit(t *testing.T) (*httptest.Server, *atomic.Bool) {
	up := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			// Closing without a reply is as close to "connection
			// refused" as a test server gets.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
		}
		_, _ = w.Write([]byte("Ok."))
	}))
	t.Cleanup(srv.Close)
	return srv, up
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A downloader left Unreachable by a failed apply comes back the moment
// a connection test shows it is reachable, from the row or the form.
func TestConnectionTestRecoversAFailingDownloader(t *testing.T) {
	for _, via := range []string{"row", "form"} {
		t.Run(via, func(t *testing.T) {
			app := newApp(t)
			qbit, up := restartingQbit(t)
			id, err := app.db.CreateDownloader(db.Downloader{Name: "qBit", Type: "qbittorrent", URL: qbit.URL, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			app.sched.ResolveAsync()
			waitUntil(t, "the apply to fail", func() bool { return app.sched.State(id).Failure != "" })
			expectBody(t, app.get("/configuration/connections/status"), `aria-label="Unreachable. Unable to connect"`)

			up.Store(true)
			var rec *httptest.ResponseRecorder
			if via == "row" {
				rec = app.post(fmt.Sprintf("/configuration/downloaders/%d/test", id), nil)
			} else {
				rec = app.post("/configuration/downloaders/test", map[string][]string{
					"id": {fmt.Sprint(id)}, "name": {"qBit"}, "type": {"qbittorrent"}, "url": {qbit.URL},
				})
			}
			expectBody(t, rec, `data-state="connected"`)
			waitUntil(t, "the rates to go back on", func() bool { st := app.sched.State(id); return st.Known && st.Failure == "" })
			body := app.get("/configuration/connections/status").Body.String()
			if !strings.Contains(body, fmt.Sprintf(`id="st-dl-%d"`, id)) || !strings.Contains(body, `aria-label="Default rates. Nothing mapped is playing"`) {
				t.Errorf("status after recovery: %s", body)
			}
		})
	}
}

// The Connections page follows the live stream for its dots.
func TestConnectionsDotsAreLive(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("JF", "jellyfin", "http://jf.lan")
	dl := app.downloader("qBit")
	page := app.get("/configuration?tab=connections").Body.String()
	for _, want := range []string{
		`id="conn-live"`, `data-live="dashboard"`,
		fmt.Sprintf(`id="st-ms-%d"`, ms), fmt.Sprintf(`id="st-dl-%d"`, dl), fmt.Sprintf(`id="sess-ms-%d"`, ms),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Connections page lacks %s", want)
		}
	}
	status := app.get("/configuration/connections/status").Body.String()
	if !strings.Contains(status, fmt.Sprintf(`<span id="sess-ms-%d">0</span>`, ms)) || strings.Contains(status, "<table") {
		t.Errorf("status fragment: %s", status)
	}
}
