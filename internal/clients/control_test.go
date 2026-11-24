package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// fakeQBittorrent holds qBittorrent's speed settings the way the WebUI
// API reports them: limits in bytes/s, the mode as 0 or 1.
type fakeQBittorrent struct {
	mu        sync.Mutex
	prefs     map[string]any
	alt       bool
	oldAPI    bool // before 4.5: no setSpeedLimitsMode
	toggles   int
	transfers int // calls to the transfer API's limit setters
}

func newFakeQBittorrent(t *testing.T) (*fakeQBittorrent, *httptest.Server) {
	t.Helper()
	f := &fakeQBittorrent{prefs: map[string]any{
		"dl_limit": float64(0), "up_limit": float64(0),
		"alt_dl_limit": float64(10240), "alt_up_limit": float64(5120),
		"scheduler_enabled": false,
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/app/preferences":
			_ = json.NewEncoder(w).Encode(f.prefs)
		case "/api/v2/app/setPreferences":
			var set map[string]any
			if err := json.Unmarshal([]byte(r.FormValue("json")), &set); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			for k, v := range set {
				f.prefs[k] = v
			}
		case "/api/v2/transfer/speedLimitsMode":
			if f.alt {
				_, _ = w.Write([]byte("1"))
			} else {
				_, _ = w.Write([]byte("0"))
			}
		case "/api/v2/transfer/setSpeedLimitsMode":
			if f.oldAPI {
				http.NotFound(w, r)
				return
			}
			f.alt = r.FormValue("mode") != "0"
		case "/api/v2/transfer/toggleSpeedLimitsMode":
			f.alt = !f.alt
			f.toggles++
		case "/api/v2/transfer/setDownloadLimit", "/api/v2/transfer/setUploadLimit":
			f.transfers++
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeQBittorrent) pref(key string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prefs[key]
}

// With alternative speed on, the transfer API's setters write the
// alternative limits; the ordinary limits go through the preferences by
// name instead, leaving the user's alternative ones alone.
func TestQBittorrentLimitsLeaveAlternativeSpeedAlone(t *testing.T) {
	f, srv := newFakeQBittorrent(t)
	f.alt = true
	if err := SetQBittorrentLimits(context.Background(), srv.URL, "", "", 1500, 300); err != nil {
		t.Fatal(err)
	}
	if f.pref("dl_limit") != float64(1500*1024) || f.pref("up_limit") != float64(300*1024) {
		t.Errorf("normal limits = %v / %v", f.pref("dl_limit"), f.pref("up_limit"))
	}
	if f.pref("alt_dl_limit") != float64(10240) || f.pref("alt_up_limit") != float64(5120) {
		t.Errorf("alternative limits changed: %v / %v", f.pref("alt_dl_limit"), f.pref("alt_up_limit"))
	}
	if !f.alt || f.transfers != 0 {
		t.Errorf("alternative on = %v, transfer setter calls = %d", f.alt, f.transfers)
	}
}

func TestQBittorrentInspect(t *testing.T) {
	f, srv := newFakeQBittorrent(t)
	f.prefs["dl_limit"] = float64(2048 * 1024)
	f.prefs["scheduler_enabled"] = true
	f.alt = true
	got, err := QBittorrent{}.Inspect(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := ClientState{
		Normal:        Limits{DownloadKBps: 2048},
		Alternative:   Limits{DownloadKBps: 10, UploadKBps: 5},
		AlternativeOn: true,
		Scheduler:     "alternative speed schedule",
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestQBittorrentApplyAlternative(t *testing.T) {
	for _, oldAPI := range []bool{false, true} {
		t.Run("old API "+strconv.FormatBool(oldAPI), func(t *testing.T) {
			f, srv := newFakeQBittorrent(t)
			f.oldAPI = oldAPI
			on := true
			want := Desired{Normal: Limits{DownloadKBps: 9000}, Alternative: &Limits{DownloadKBps: 1000, UploadKBps: 200}, AlternativeOn: &on}
			if err := (QBittorrent{}).Apply(context.Background(), srv.URL, "", "", want); err != nil {
				t.Fatal(err)
			}
			if f.pref("alt_dl_limit") != float64(1000*1024) || f.pref("alt_up_limit") != float64(200*1024) || !f.alt {
				t.Errorf("alternative = %v / %v, on %v", f.pref("alt_dl_limit"), f.pref("alt_up_limit"), f.alt)
			}
			got, err := QBittorrent{}.Inspect(context.Background(), srv.URL, "", "")
			if err != nil || !want.Matches(got) {
				t.Errorf("read back %+v, %v", got, err)
			}
			// Already on: an old qBittorrent isn't toggled back off.
			if err := (QBittorrent{}).Apply(context.Background(), srv.URL, "", "", want); err != nil || !f.alt {
				t.Errorf("second apply: on %v, %v", f.alt, err)
			}
			if oldAPI && f.toggles != 1 {
				t.Errorf("toggles = %d, want 1", f.toggles)
			}
		})
	}
}

func TestQBittorrentClearConflicts(t *testing.T) {
	f, srv := newFakeQBittorrent(t)
	f.prefs["scheduler_enabled"] = true
	f.prefs["schedule_from_hour"] = float64(8)
	f.alt = true
	if err := (QBittorrent{}).ClearConflicts(context.Background(), srv.URL, "", ""); err != nil {
		t.Fatal(err)
	}
	if f.pref("scheduler_enabled") != false || f.alt {
		t.Errorf("scheduler %v, alternative on %v", f.pref("scheduler_enabled"), f.alt)
	}
	if f.pref("schedule_from_hour") != float64(8) {
		t.Error("the schedule's times were changed")
	}
}

// A limit qBittorrent can't hold as a 32-bit count of bytes would wrap to
// unlimited, so it's capped instead.
func TestQBittorrentCapsHugeLimits(t *testing.T) {
	f, srv := newFakeQBittorrent(t)
	if err := SetQBittorrentLimits(context.Background(), srv.URL, "", "", 5_000_000, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.pref("dl_limit").(float64); got <= 0 || got > 1<<31-1 {
		t.Errorf("dl_limit = %v", got)
	}
}

func TestTransmissionInspect(t *testing.T) {
	// 4.1.0 sent the speed limits as decimals.
	_, srv := newTransmissionStub(t, `{"speed-limit-down":500.0,"speed-limit-down-enabled":true,
		"speed-limit-up":100,"speed-limit-up-enabled":false,"alt-speed-down":50,"alt-speed-up":20,
		"alt-speed-enabled":true,"alt-speed-time-enabled":true}`)
	got, err := Transmission{}.Inspect(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := ClientState{
		Normal:        Limits{DownloadKBps: 500},
		Alternative:   Limits{DownloadKBps: 50, UploadKBps: 20},
		AlternativeOn: true,
		Scheduler:     "alternative speed schedule",
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	_, srv = newTransmissionStub(t, `{"speed-limit-down":500}`)
	if _, err := (Transmission{}).Inspect(context.Background(), srv.URL, "", ""); err == nil {
		t.Error("a session missing fields: want an error")
	}
}

func TestTransmissionApplyAndClear(t *testing.T) {
	stub, srv := newTransmissionStub(t, `{}`)
	on := true
	want := Desired{Normal: Limits{DownloadKBps: 900}, Alternative: &Limits{DownloadKBps: 100, UploadKBps: 0}, AlternativeOn: &on}
	if err := (Transmission{}).Apply(context.Background(), srv.URL, "", "", want); err != nil {
		t.Fatal(err)
	}
	args := stub.requests[len(stub.requests)-1].Arguments.(map[string]any)
	if args["alt-speed-down"] != float64(100) || args["alt-speed-up"] != float64(0) || args["alt-speed-enabled"] != true || args["speed-limit-down"] != float64(900) {
		t.Errorf("session-set = %v", args)
	}

	if err := (Transmission{}).ClearConflicts(context.Background(), srv.URL, "", ""); err != nil {
		t.Fatal(err)
	}
	args = stub.requests[len(stub.requests)-1].Arguments.(map[string]any)
	if args["alt-speed-time-enabled"] != false || args["alt-speed-enabled"] != false || len(args) != 2 {
		t.Errorf("clearing sent %v", args)
	}
}

func TestDelugeInspect(t *testing.T) {
	f, srv := newFakeDeluge(t)
	f.config["max_download_speed"] = 1500
	got, err := Deluge{}.Inspect(context.Background(), srv.URL, "", "deluge")
	if err != nil {
		t.Fatal(err)
	}
	if got != (ClientState{Normal: Limits{DownloadKBps: 1500}}) {
		t.Errorf("without the plugin: %+v", got)
	}

	// Enabled but all green changes nothing; one slow hour fights us.
	f.plugins = []string{"Label", "Scheduler"}
	f.grid = make([][]int, 24)
	for h := range f.grid {
		f.grid[h] = make([]int, 7)
	}
	if got, err = (Deluge{}).Inspect(context.Background(), srv.URL, "", "deluge"); err != nil || got.Scheduler != "" {
		t.Errorf("all green: %+v, %v", got, err)
	}
	f.grid[3][5] = 1
	if got, err = (Deluge{}).Inspect(context.Background(), srv.URL, "", "deluge"); err != nil || got.Scheduler != "Scheduler plugin" {
		t.Errorf("one slow hour: %+v, %v", got, err)
	}
}

func TestDelugeClearConflictsResumesARedHour(t *testing.T) {
	f, srv := newFakeDeluge(t)
	f.plugins = []string{"Scheduler", "Label"}
	f.paused = true
	if err := (Deluge{}).ClearConflicts(context.Background(), srv.URL, "", "deluge"); err != nil {
		t.Fatal(err)
	}
	if f.schedulerOn() || f.paused || len(f.plugins) != 1 {
		t.Errorf("plugins %v, paused %v", f.plugins, f.paused)
	}

	// Nothing to clear: nothing is called beyond the check.
	f2, srv2 := newFakeDeluge(t)
	if err := (Deluge{}).ClearConflicts(context.Background(), srv2.URL, "", "deluge"); err != nil {
		t.Fatal(err)
	}
	if f2.called("core.disable_plugin") || f2.called("core.resume_session") {
		t.Error("cleared a scheduler that wasn't on")
	}
}

func TestEveryDownloaderTypeIsAController(t *testing.T) {
	for _, dlType := range []string{"qbittorrent", "transmission", "deluge"} {
		if ControllerFor(dlType) == nil {
			t.Errorf("ControllerFor(%q) = nil", dlType)
		}
	}
}
