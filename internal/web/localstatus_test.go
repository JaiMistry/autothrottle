package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// localGet asks for the tray's status as a client at remoteAddr would.
func (a *testApp) localGet(remoteAddr string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "http://127.0.0.1:7676/local/status", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func (a *testApp) enableLocalStatus() {
	a.srv.EnableLocalStatus()
	a.handler = a.srv.Router()
}

func decodeLocalStatus(t *testing.T, rec *httptest.ResponseRecorder) localStatus {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
	}
	var status localStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("not JSON: %v: %s", err, rec.Body.String())
	}
	return status
}

// Off unless the Windows build turns it on, so Docker installs serve
// nothing new. Refused, it is any dead link: signed out, as the tray is,
// that is the sign-in page.
func TestLocalStatusIsOffByDefault(t *testing.T) {
	app := newApp(t)
	if rec := app.localGet("127.0.0.1:50000", nil); rec.Code == http.StatusOK {
		t.Errorf("answered %d without being enabled", rec.Code)
	}
}

func TestLocalStatusAnswersThisMachineOnly(t *testing.T) {
	app := newApp(t)
	app.enableLocalStatus()

	for _, addr := range []string{"127.0.0.1:50000", "[::1]:50000"} {
		status := decodeLocalStatus(t, app.localGet(addr, nil))
		if status.Setup || status.Statement != "Not ready to throttle" {
			t.Errorf("%s: %+v", addr, status)
		}
	}
	if rec := app.localGet("192.168.1.20:50000", nil); rec.Code == http.StatusOK {
		t.Errorf("another machine got %d", rec.Code)
	}
	// A reverse proxy on the same machine connects from loopback too.
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded"} {
		if rec := app.localGet("127.0.0.1:50000", map[string]string{h: "203.0.113.9"}); rec.Code == http.StatusOK {
			t.Errorf("a request forwarded with %s got %d", h, rec.Code)
		}
	}
	// Addressed by any other name: a page using DNS rebinding, or a proxy.
	for _, host := range []string{"evil.example", "evil.example:7676", "192.168.1.5:7676"} {
		if rec := app.localGet("127.0.0.1:50000", map[string]string{"Host": host}); rec.Code == http.StatusOK {
			t.Errorf("Host %s got %d", host, rec.Code)
		}
	}
	for _, host := range []string{"localhost:7676", "LOCALHOST", "[::1]:7676", "127.0.0.1"} {
		if rec := app.localGet("127.0.0.1:50000", map[string]string{"Host": host}); rec.Code != http.StatusOK {
			t.Errorf("Host %s got %d", host, rec.Code)
		}
	}
	if rec := app.localGet("not-an-address", nil); rec.Code == http.StatusOK {
		t.Errorf("an unparseable address got %d", rec.Code)
	}
}

func TestLocalStatusReportsSetupAndPause(t *testing.T) {
	fresh := newAppWith(t, nil)
	fresh.enableLocalStatus()
	if status := decodeLocalStatus(t, fresh.localGet("127.0.0.1:50000", nil)); !status.Setup || status.Statement != "Not set up yet" {
		t.Errorf("before setup: %+v", status)
	}

	app := newApp(t)
	app.enableLocalStatus()
	app.sched.SetPaused(context.Background(), true)
	if status := decodeLocalStatus(t, app.localGet("127.0.0.1:50000", nil)); !status.Paused {
		t.Errorf("paused: %+v", status)
	}
}

// Docker installs are told how to update; on Windows the button
// downloads the new installer instead.
func TestAvailableUpdate(t *testing.T) {
	docker := available("linux", "1.0.1", "1.1.0")
	if docker.NotePost != ". Pull the new image and restart." || docker.Download != "" {
		t.Errorf("linux: %+v", docker)
	}
	win := available("windows", "1.0.1", "1.1.0")
	if win.NotePost != "." || win.Download != "https://github.com/JaiMistry/autothrottle/releases/download/v1.1.0/AutoThrottle-Setup-1.1.0.exe" {
		t.Errorf("windows: %+v", win)
	}
	if win.State != "available" || win.Label != "v1.1.0 available" || win.NoteStrong != "v1.0.1" {
		t.Errorf("windows lost the common parts: %+v", win)
	}
}

func TestPlatformWords(t *testing.T) {
	if authNote("windows") != "Created on first run" || authNote("linux") != "Set via environment variables at deploy time" {
		t.Errorf("auth notes: %q, %q", authNote("windows"), authNote("linux"))
	}
	if got := logHint("windows", `C:\ProgramData`); !strings.Contains(got, "AutoThrottle") || !strings.HasPrefix(got, "Check the log in ") {
		t.Errorf("windows log hint: %q", got)
	}
	if got := logHint("linux", ""); got != "Check the container log." {
		t.Errorf("linux log hint: %q", got)
	}
}

// localPost switches throttling as the tray's menu does, from this
// machine with no browser headers unless header adds some.
func (a *testApp) localPost(paused string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "http://127.0.0.1:7676/local/throttling", strings.NewReader("paused="+paused))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:50000"
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

// Off only once there is something to throttle, as on the Dashboard;
// back on whenever it is off.
func TestTheTrayTurnsThrottlingOffOnlyWhenReady(t *testing.T) {
	app := newApp(t)
	app.enableLocalStatus()
	if status := decodeLocalStatus(t, app.localGet("127.0.0.1:50000", nil)); status.Ready {
		t.Errorf("ready with nothing set up: %+v", status)
	}
	if rec := app.localPost("true", nil); rec.Code != http.StatusConflict || app.sched.Paused() {
		t.Errorf("turned off with nothing to throttle: %d, paused %v", rec.Code, app.sched.Paused())
	}
	app.sched.SetPaused(context.Background(), true)
	if rec := app.localPost("false", nil); rec.Code != http.StatusNoContent || app.sched.Paused() {
		t.Errorf("turning it back on with nothing to throttle: %d, paused %v", rec.Code, app.sched.Paused())
	}

	app.mediaServer("Plex", "plex", "http://plex.lan:32400")
	if rec := app.localPost("true", nil); rec.Code != http.StatusConflict {
		t.Errorf("turned off with a media server alone: %d", rec.Code)
	}
	app.downloader("qBittorrent")
	if status := decodeLocalStatus(t, app.localGet("127.0.0.1:50000", nil)); !status.Ready {
		t.Errorf("not ready with both: %+v", status)
	}
}

func TestTheTrayTurnsThrottlingOffAndOn(t *testing.T) {
	app := newApp(t)
	app.enableLocalStatus()
	app.mediaServer("Plex", "plex", "http://plex.lan:32400")
	app.downloader("qBittorrent")

	if rec := app.localPost("true", nil); rec.Code != http.StatusNoContent || !app.sched.Paused() {
		t.Fatalf("turning it off: %d, paused %v", rec.Code, app.sched.Paused())
	}
	// Setting, not toggling: the same request twice leaves it off.
	if rec := app.localPost("true", nil); rec.Code != http.StatusNoContent || !app.sched.Paused() {
		t.Errorf("asking again: %d, paused %v", rec.Code, app.sched.Paused())
	}
	if rec := app.localPost("false", nil); rec.Code != http.StatusNoContent || app.sched.Paused() {
		t.Errorf("turning it back on: %d, paused %v", rec.Code, app.sched.Paused())
	}
	if rec := app.localPost("maybe", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a muddled request got %d", rec.Code)
	}
}

// Only the tray: never a browser, another machine, or a rebound hostname.
func TestOnlyTheTrayCanSwitchThrottling(t *testing.T) {
	app := newApp(t)
	app.enableLocalStatus()
	app.mediaServer("Plex", "plex", "http://plex.lan:32400")
	app.downloader("qBittorrent")
	for name, header := range map[string]map[string]string{
		"a cross-site form":         {"Origin": "https://evil.example"},
		"a page on this machine":    {"Origin": "http://localhost:7676"},
		"a browser without Origin":  {"Sec-Fetch-Site": "cross-site"},
		"a rebound hostname":        {"Host": "evil.example:7676"},
		"a request through a proxy": {"X-Forwarded-For": "203.0.113.9"},
	} {
		if rec := app.localPost("true", header); rec.Code == http.StatusNoContent || app.sched.Paused() {
			t.Errorf("%s got %d, paused %v", name, rec.Code, app.sched.Paused())
		}
	}

	off := newApp(t)
	if rec := off.localPost("true", nil); rec.Code == http.StatusNoContent || off.sched.Paused() {
		t.Errorf("answered %d without being enabled", rec.Code)
	}

	fresh := newAppWith(t, nil)
	fresh.enableLocalStatus()
	if rec := fresh.localPost("true", nil); rec.Code != http.StatusConflict {
		t.Errorf("before setup: %d", rec.Code)
	}
}
