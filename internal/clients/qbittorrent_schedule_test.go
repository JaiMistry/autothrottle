package clients

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type scheduleMemoryStore struct {
	value string
	fail  bool
}

func (s *scheduleMemoryStore) GetSetting(string) (string, bool, error) {
	return s.value, s.value != "", nil
}
func (s *scheduleMemoryStore) SetSetting(_ string, value string) error {
	if s.fail {
		return errors.New("disk unavailable")
	}
	s.value = value
	return nil
}

func TestQBittorrentSchedulePreservesBothBanksAcrossModeChanges(t *testing.T) {
	rates := map[string]int64{"dl_limit": 0, "up_limit": 0, "alt_dl_limit": 15000 * 1024, "alt_up_limit": 50 * 1024}
	original := make(map[string]int64)
	for k, v := range rates {
		original[k] = v
	}
	mode, writes := 1, 0
	store := &scheduleMemoryStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/preferences":
			prefs := map[string]any{"scheduler_enabled": true}
			for k, v := range rates {
				prefs[k] = v
			}
			_ = json.NewEncoder(w).Encode(prefs)
		case "/api/v2/transfer/speedLimitsMode":
			_ = json.NewEncoder(w).Encode(mode)
		case "/api/v2/app/setPreferences":
			if store.value == "" {
				t.Error("rate change attempted before durable recovery was saved")
			}
			var patch map[string]int64
			if err := json.Unmarshal([]byte(r.FormValue("json")), &patch); err != nil {
				t.Error(err)
			}
			for k, v := range patch {
				if _, ok := rates[k]; !ok {
					t.Errorf("modified a scheduler or unknown field: %s", k)
				}
				rates[k] = v
			}
			writes++
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	apply := func(throttle bool) *AppliedLimits {
		t.Helper()
		got, err := (QBittorrent{}).ApplyScheduledLimits(context.Background(), srv.URL, "", "", throttle, 7500, 25, store, "key")
		if err != nil || got == nil {
			t.Fatalf("scheduled apply = %+v, %v", got, err)
		}
		return got
	}
	if got := apply(false); writes != 0 || got.DownloadKBps != 15000 {
		t.Fatalf("idle apply wrote rates: %+v, writes=%d", got, writes)
	}
	apply(true)
	for _, field := range []string{"dl_limit", "alt_dl_limit"} {
		if rates[field] != 7500*1024 {
			t.Fatalf("uncapped bank: %v", rates)
		}
	}
	for _, field := range []string{"up_limit", "alt_up_limit"} {
		if rates[field] != 25*1024 {
			t.Fatalf("uncapped bank: %v", rates)
		}
	}
	// The scheduler switches modes while AutoThrottle is stopped/restarted.
	mode = 0
	apply(true)
	apply(false)
	if !reflect.DeepEqual(rates, original) || store.value != "" {
		t.Fatalf("wrong recovery after mode change/restart: %v, %s", rates, store.value)
	}
	mode = 1
	apply(true)
	// A user lowers an alternative rate while playing; release must keep it.
	rates["alt_up_limit"] = 20 * 1024
	apply(false)
	if rates["dl_limit"] != 0 || rates["alt_dl_limit"] != 15000*1024 || rates["alt_up_limit"] != 20*1024 {
		t.Fatalf("external edit overwritten: %v", rates)
	}
}

func TestQBittorrentScheduleNeverRaisesStricterLimits(t *testing.T) {
	rates := map[string]int64{"dl_limit": 100 * 1024, "up_limit": 10 * 1024, "alt_dl_limit": 0, "alt_up_limit": 0}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/preferences":
			p := map[string]any{"scheduler_enabled": true}
			for k, v := range rates {
				p[k] = v
			}
			_ = json.NewEncoder(w).Encode(p)
		case "/api/v2/transfer/speedLimitsMode":
			_, _ = w.Write([]byte("0"))
		case "/api/v2/app/setPreferences":
			var p map[string]int64
			_ = json.Unmarshal([]byte(r.FormValue("json")), &p)
			for k, v := range p {
				rates[k] = v
			}
		}
	}))
	defer srv.Close()
	s := &scheduleMemoryStore{}
	got, err := (QBittorrent{}).ApplyScheduledLimits(context.Background(), srv.URL, "", "", true, 500, 100, s, "k")
	if err != nil || got.DownloadKBps != 100 || got.UploadKBps != 10 || rates["alt_dl_limit"] != 500*1024 {
		t.Fatalf("limits = %+v, %v, %v", got, err, rates)
	}
}

func TestQBittorrentScheduleFailsSafely(t *testing.T) {
	for _, tc := range []struct {
		name, body            string
		failStore, failRemote bool
	}{
		{"invalid response", `{}`, false, false},
		{"missing rate", `{"scheduler_enabled":true}`, false, false},
		{"null rate", `{"scheduler_enabled":true,"dl_limit":null,"up_limit":0,"alt_dl_limit":1,"alt_up_limit":1}`, false, false},
		{"storage failure", `{"scheduler_enabled":true,"dl_limit":0,"up_limit":0,"alt_dl_limit":1,"alt_up_limit":1}`, true, false},
		{"remote failure", `{"scheduler_enabled":true,"dl_limit":0,"up_limit":0,"alt_dl_limit":1,"alt_up_limit":1}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/app/preferences":
					_, _ = w.Write([]byte(tc.body))
				case "/api/v2/transfer/speedLimitsMode":
					_, _ = w.Write([]byte("1"))
				case "/api/v2/app/setPreferences":
					writes++
					if tc.failRemote {
						w.WriteHeader(500)
					}
				}
			}))
			defer srv.Close()
			store := &scheduleMemoryStore{fail: tc.failStore}
			_, err := (QBittorrent{}).ApplyScheduledLimits(context.Background(), srv.URL, "", "", true, 500, 100, store, "k")
			if err == nil {
				t.Fatal("unsafe response accepted")
			}
			if !tc.failRemote && writes != 0 {
				t.Fatal("wrote without a valid persisted baseline")
			}
			if tc.failRemote && store.value == "" {
				t.Fatal("lost recovery after a failed write")
			}
		})
	}
}

func TestQBittorrentScheduleOffKeepsFixedRatePolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"scheduler_enabled":false}`)) }))
	defer srv.Close()
	got, err := (QBittorrent{}).ApplyScheduledLimits(context.Background(), srv.URL, "", "", false, 100, 10, &scheduleMemoryStore{}, "k")
	if err != nil || got != nil {
		t.Fatalf("manual policy: %+v, %v", got, err)
	}
}
