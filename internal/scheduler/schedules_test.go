package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

type scheduledQbit struct {
	mu     sync.Mutex
	rates  map[string]int64
	mode   int
	writes int
}

func (q *scheduledQbit) snapshot() map[string]int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[string]int64)
	for k, v := range q.rates {
		out[k] = v
	}
	return out
}

func scheduleFixture(t *testing.T) (*Scheduler, *db.DB, *scheduledQbit, int64, int64) {
	t.Helper()
	s, d, _ := newTestScheduler(t)
	s.limiterFor = clients.RateLimiterFor
	q := &scheduledQbit{rates: map[string]int64{"dl_limit": 2000 * 1024, "up_limit": 20 * 1024, "alt_dl_limit": 100 * 1024, "alt_up_limit": 10 * 1024}, mode: 1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		switch r.URL.Path {
		case "/api/v2/app/preferences":
			p := map[string]any{"scheduler_enabled": true}
			for k, v := range q.rates {
				p[k] = v
			}
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v2/transfer/speedLimitsMode":
			_ = json.NewEncoder(w).Encode(q.mode)
		case "/api/v2/app/setPreferences":
			var patch map[string]int64
			if err := json.Unmarshal([]byte(r.FormValue("json")), &patch); err != nil {
				t.Error(err)
			}
			for k, v := range patch {
				if _, ok := q.rates[k]; !ok {
					t.Errorf("unexpected preference %s", k)
				}
				q.rates[k] = v
			}
			q.writes++
		default:
			t.Errorf("unexpected fixed-rate request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	msID, dlID := addServerAndDownloader(t, d)
	dl, err := d.GetDownloader(dlID)
	if err != nil {
		t.Fatal(err)
	}
	dl.URL = srv.URL
	if err := d.UpdateDownloader(dl); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}
	return s, d, q, msID, dlID
}

func TestScheduledRatesFollowModeAndRecoverAfterRestart(t *testing.T) {
	s, d, q, ms, dl := scheduleFixture(t)
	original := q.snapshot()
	s.resolve(context.Background())
	if !reflect.DeepEqual(q.snapshot(), original) || s.State(dl).DownloadKBps != 100 {
		t.Fatal("idle startup overwrote scheduled rates")
	}
	s.HandleSessionEvent(ms, clients.Session{Key: "viewer"}, true)
	s.resolve(context.Background())
	if q.snapshot()["dl_limit"] != 500*1024 || q.snapshot()["alt_dl_limit"] != 100*1024 {
		t.Fatal("failed to cap both banks without raising a stricter rate")
	}
	q.mu.Lock()
	q.mode = 0
	q.mu.Unlock()
	s.resolveWithSchedules(context.Background(), true)
	if s.State(dl).DownloadKBps != 500 || s.State(dl).UploadKBps != 20 {
		t.Fatalf("wrong current-mode display: %+v", s.State(dl))
	}
	restarted := New(d, make([]byte, 32), settings.New(d))
	restarted.HandleSessionEvent(ms, clients.Session{Key: "viewer"}, true)
	restarted.resolve(context.Background())
	restarted.HandleSessionEvent(ms, clients.Session{Key: "viewer"}, false)
	restarted.resolve(context.Background())
	if !reflect.DeepEqual(q.snapshot(), original) {
		t.Fatalf("restart restored wrong rates: %v", q.snapshot())
	}
	value, _, err := d.GetSetting(scheduleStateKey(dl))
	if err != nil || value != "" {
		t.Fatalf("recovery state not cleared: %s, %v", value, err)
	}
}

func TestScheduledRatesRestoredOnShutdownDisableAndDelete(t *testing.T) {
	for _, action := range []string{"shutdown", "disable", "delete"} {
		t.Run(action, func(t *testing.T) {
			s, d, q, ms, dl := scheduleFixture(t)
			original := q.snapshot()
			s.HandleSessionEvent(ms, clients.Session{Key: "viewer"}, true)
			s.resolve(context.Background())
			var err error
			switch action {
			case "shutdown":
				err = s.Shutdown(context.Background())
			case "disable":
				var next db.Downloader
				next, err = d.GetDownloader(dl)
				if err == nil {
					next.Enabled = false
					err = s.UpdateDownloader(context.Background(), next)
				}
			case "delete":
				err = s.DeleteDownloader(context.Background(), dl)
			}
			if err != nil {
				t.Fatal(err)
			}
			s.resolve(context.Background())
			if !reflect.DeepEqual(q.snapshot(), original) {
				t.Fatalf("%s left rates changed: %v", action, q.snapshot())
			}
		})
	}
}
