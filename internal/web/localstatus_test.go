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
	req := httptest.NewRequest("GET", "/local/status", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range header {
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
// nothing new.
func TestLocalStatusIsOffByDefault(t *testing.T) {
	app := newApp(t)
	if rec := app.localGet("127.0.0.1:50000", nil); rec.Code != http.StatusNotFound {
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
	if rec := app.localGet("192.168.1.20:50000", nil); rec.Code != http.StatusNotFound {
		t.Errorf("another machine got %d", rec.Code)
	}
	// A reverse proxy on the same machine connects from loopback too.
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded"} {
		if rec := app.localGet("127.0.0.1:50000", map[string]string{h: "203.0.113.9"}); rec.Code != http.StatusNotFound {
			t.Errorf("a request forwarded with %s got %d", h, rec.Code)
		}
	}
	if rec := app.localGet("not-an-address", nil); rec.Code != http.StatusNotFound {
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
