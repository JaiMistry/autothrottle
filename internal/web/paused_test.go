package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// While disabled, every page carries the banner; the Dashboard drops its
// own pause alert and button.
func TestPausedBanner(t *testing.T) {
	app := newApp(t)
	app.mediaServer("JF", "jellyfin", "http://jf.lan")
	app.downloader("qBit")

	for _, path := range []string{"/", "/configuration", "/logs"} {
		if strings.Contains(app.get(path).Body.String(), `class="at-paused"`) {
			t.Errorf("%s shows the banner while throttling", path)
		}
	}
	expectBody(t, app.get("/"), ">Disable throttling<")

	expectStatus(t, app.post("/pause-toggle", nil), http.StatusSeeOther)
	since := regexp.MustCompile(`<b>Throttling disabled</b> since \d{1,2}:\d{2} [AP]M\. Download clients restored to default speeds\.`)
	for _, path := range []string{"/", "/configuration", "/logs", "/paused-banner"} {
		body := app.get(path).Body.String()
		if !since.MatchString(body) || !strings.Contains(body, `role="status"`) || !strings.Contains(body, ">Resume throttling<") {
			t.Errorf("%s has no banner while disabled", path)
		}
	}
	dash := app.get("/").Body.String()
	if strings.Contains(dash, "Throttling is off") || strings.Contains(dash, ">Disable throttling<") {
		t.Error("the Dashboard kept its own pause alert or button")
	}
	if strings.Count(dash, ">Resume throttling<") != 1 {
		t.Error("Resume appears other than once, on the banner")
	}
	if !strings.Contains(dash, `data-tone="warn"`) || !strings.Contains(dash, "Throttling disabled") {
		t.Error("the status pill does not read Throttling disabled in warn")
	}
	if !strings.Contains(dash, `<rect x="6.5" y="6.5" width="11" height="11" rx="2" fill="currentColor"/>`) {
		t.Error("the paused pill does not carry the filled-square glyph")
	}

	rec := app.postFrom("http://example.com/logs?level=warn", "/pause-toggle")
	expectRedirect(t, rec, "/logs?level=warn")
	if app.sched.Paused() {
		t.Fatal("Resume did not resume")
	}
	if strings.Contains(app.get("/logs").Body.String(), `class="at-paused"`) {
		t.Error("the banner outlived the pause")
	}
	app.post("/pause-toggle", nil)
	expectRedirect(t, app.postFrom("https://evil.example/x", "/pause-toggle"), "/")
}
