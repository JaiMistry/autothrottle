package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// restartingQbit is down until up is set, like one restarting for an update.
func restartingQbit(t *testing.T) (*httptest.Server, *atomic.Bool) {
	up := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			// The nearest a test server gets to "connection refused".
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
		}
		if r.URL.Path == "/api/v2/app/preferences" {
			_, _ = w.Write([]byte(`{"scheduler_enabled":false}`))
			return
		}
		_, _ = w.Write([]byte("Ok."))
	}))
	t.Cleanup(srv.Close)
	return srv, up
}

// A passing connection test, from the row or the form, re-applies a
// downloader left Unreachable by a failed apply.
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
			waitFor(t, "the apply to fail", func() bool { return app.sched.State(id).Failure != "" })
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
			waitFor(t, "the rates to go back on", func() bool { st := app.sched.State(id); return st.Known && st.Failure == "" })
			body := app.get("/configuration/connections/status").Body.String()
			if !strings.Contains(body, fmt.Sprintf(`id="st-dl-%d"`, id)) || !strings.Contains(body, `aria-label="Default rates. Nothing mapped is playing"`) {
				t.Errorf("status after recovery: %s", body)
			}
		})
	}
}

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
