package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestUpgradeHint(t *testing.T) {
	if got := upgradeHint("windows"); got != "Run the new installer from the release page." {
		t.Errorf("windows: %q", got)
	}
	if got := upgradeHint("linux"); got != "Pull the new image and restart." {
		t.Errorf("linux: %q", got)
	}
}
