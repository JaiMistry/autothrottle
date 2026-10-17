package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dispatcharrLiveStatus(live string) string {
	return `{"live":` + live + `,"vod":{"vod_connections":[],"total_connections":0},"catchup":{"timeshift_sessions":[],"total_connections":0}}`
}

func writeDispatcharrLive(w http.ResponseWriter, channels []dispatcharrChannel) {
	body, _ := json.Marshal(map[string]any{"channels": channels})
	_, _ = w.Write([]byte(dispatcharrLiveStatus(string(body))))
}

func TestDispatcharrSnapshot(t *testing.T) {
	channels := []dispatcharrChannel{
		{ID: "idle", ClientCount: 0},
		{ID: "one", Name: "News", ClientCount: 3, Clients: []dispatcharrClient{
			{ID: "local", IP: "192.168.1.8:1234", UserAgent: "VLC"},
			{ID: "remote", IP: "203.0.113.4", UserAgent: "TV"},
			{ID: "unknown", IP: "unknown"},
		}},
		{ID: "two", Name: "Sport", ClientCount: 1, Clients: []dispatcharrClient{{ID: "remote", IP: "2001:db8::1"}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/proxy/stats/" || r.Header.Get("X-API-Key") != "secret" || r.URL.RawQuery != "" {
			t.Errorf("wrong request path or authentication: %s", r.URL.Path)
		}
		writeDispatcharrLive(w, channels)
	}))
	defer srv.Close()
	watcher := NewDispatcharrWatcher(srv.URL+"/prefix/", "secret", false)
	sessions, err := watcher.Snapshot(context.Background())
	if err != nil || len(sessions) != 4 {
		t.Fatalf("snapshot = %+v, %v", sessions, err)
	}
	if sessions[0].Key != "one/local" || !sessions[0].Local || sessions[0].Title != "News" || sessions[0].Device != "VLC" {
		t.Fatalf("local session = %+v", sessions[0])
	}
	watcher.SkipLocalPlayback = true
	sessions, err = watcher.Snapshot(context.Background())
	if err != nil || len(sessions) != 3 || sessions[0].Local || sessions[1].Key != "one/unknown" || sessions[2].Key != "two/remote" {
		t.Fatalf("filtered snapshot = %+v, %v", sessions, err)
	}
}

func TestDispatcharrTruncatedClients(t *testing.T) {
	var detailCalls atomic.Int32
	clients := make([]dispatcharrClient, 0, 11)
	for i := 0; i < 10; i++ {
		clients = append(clients, dispatcharrClient{ID: string(rune('a' + i)), IP: "10.0.0.1"})
	}
	clients = append(clients, dispatcharrClient{ID: "remote", IP: "203.0.113.7"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/ts/status/channel" {
			detailCalls.Add(1)
			_ = json.NewEncoder(w).Encode(dispatcharrChannel{ID: "channel", ClientCount: 11, Clients: clients})
			return
		}
		writeDispatcharrLive(w, []dispatcharrChannel{{ID: "channel", ClientCount: 11, Clients: clients[:10]}})
	}))
	defer srv.Close()
	w := NewDispatcharrWatcher(srv.URL, "key", true)
	sessions, err := w.Snapshot(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].Key != "channel/remote" || detailCalls.Load() != 1 {
		t.Fatalf("truncated snapshot = %+v, %v; detail calls = %d", sessions, err, detailCalls.Load())
	}
}

func TestDispatcharrRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"wrong key", `{"detail":"Invalid API key"}`, 401},
		{"non-admin key", `{}`, 403},
		{"outage", `{}`, 500},
		{"wrong path", `<html>Dispatcharr</html>`, 200},
		{"wrong schema", `{}`, 200},
		{"live-only schema", `{"channels":[]}`, 200},
		{"null list", dispatcharrLiveStatus(`{"channels":null}`), 200},
		{"missing channel ID", dispatcharrLiveStatus(`{"channels":[{"client_count":1}]}`), 200},
		{"missing client ID", dispatcharrLiveStatus(`{"channels":[{"channel_id":"c","client_count":1,"clients":[{}]}]}`), 200},
		{"duplicate client", dispatcharrLiveStatus(`{"channels":[{"channel_id":"c","client_count":2,"clients":[{"client_id":"a"},{"client_id":"a"}]}]}`), 200},
		{"missing VOD", `{"live":{"channels":[]},"catchup":{"timeshift_sessions":[]}}`, 200},
		{"missing catch-up", `{"live":{"channels":[]},"vod":{"vod_connections":[]}}`, 200},
		{"null VOD", `{"live":{"channels":[]},"vod":{"vod_connections":null},"catchup":{"timeshift_sessions":[]}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if err := TestMediaServer(context.Background(), "dispatcharr", srv.URL, "key"); err == nil {
				t.Fatal("connection test accepted invalid status")
			}
		})
	}
}

func TestDispatcharrPollEventsAndRecovery(t *testing.T) {
	var body atomic.Value
	body.Store(dispatcharrLiveStatus(`{"channels":[]}`))
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	w := NewDispatcharrWatcher(srv.URL, "key", false)
	w.pollInterval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	defer func() { cancel(); <-done }()
	const playing = `{"channels":[{"channel_id":"c","channel_name":"News","client_count":1,"clients":[{"client_id":"a","ip_address":"203.0.113.1"}]}]}`
	body.Store(dispatcharrLiveStatus(playing))
	select {
	case event := <-w.Events():
		if !event.Active || event.Key != "c/a" || !w.Connection().Connected {
			t.Fatalf("start = %+v, connection = %+v", event, w.Connection())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no start event")
	}
	// Failure does not emit a stop; only a successful empty snapshot does.
	fail.Store(true)
	deadline := time.After(time.Second)
	for w.Connection().Connected {
		select {
		case <-deadline:
			t.Fatal("failed poll still connected")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case event := <-w.Events():
		t.Fatalf("outage emitted a session change: %+v", event)
	default:
	}
	body.Store(dispatcharrLiveStatus(`{"channels":[]}`))
	fail.Store(false)
	select {
	case event := <-w.Events():
		if event.Active || event.Key != "c/a" {
			t.Fatalf("stop = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no stop after recovery")
	}
}

func TestDispatcharrVODAndCatchup(t *testing.T) {
	const body = `{
		"live":{"channels":[{"channel_id":"movie","client_count":1,"clients":[{"client_id":"viewer"}]}]},
		"vod":{"total_connections":2,"vod_connections":[
			{"content_type":"movie","content_uuid":"movie","content_name":"Film","connection_count":2,
			 "content_metadata":{"duration_secs":3600},"connections":[
				{"client_id":"viewer","client_ip":"203.0.113.1","user_agent":"VLC","position_seconds":125},
				{"client_id":"local","client_ip":"10.0.0.1"}]}]},
		"catchup":{"total_connections":2,"timeshift_sessions":[
			{"session_id":"movie","channel_name":"Earlier news","connection_count":2,
			 "paused":true,"playback_base_secs":180,"position_anchor_at":1,"connections":[
				{"client_id":"viewer","ip_address":"203.0.113.2","user_agent":"TV"},
				{"client_id":"local","ip_address":"192.168.1.2"}]}]}}
	`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	w := NewDispatcharrWatcher(srv.URL, "key", true)
	sessions, err := w.Snapshot(context.Background())
	if err != nil || len(sessions) != 3 {
		t.Fatalf("combined snapshot = %+v, %v", sessions, err)
	}
	if sessions[1].Key != "vod/movie/movie/viewer" || sessions[1].Title != "Film" || sessions[1].Device != "VLC" || sessions[1].Position != 125*time.Second || sessions[1].Duration != time.Hour {
		t.Fatalf("VOD session = %+v", sessions[1])
	}
	if sessions[2].Key != "catchup/movie/viewer" || !sessions[2].Paused || sessions[2].Position != 180*time.Second {
		t.Fatalf("paused catch-up must remain active with fixed position: %+v", sessions[2])
	}
	w.update(context.Background(), sessions)
	for range 3 {
		if event := <-w.Events(); !event.Active {
			t.Fatalf("reported client did not start throttling: %+v", event)
		}
	}
	updated := append([]Session(nil), sessions...)
	updated[2].Paused = false
	updated[2].Position += time.Second
	w.update(context.Background(), updated)
	if event := <-w.Events(); !event.DetailOnly || event.Paused {
		t.Fatalf("pause/position update = %+v", event)
	}
	w.update(context.Background(), nil)
	for range 3 {
		if event := <-w.Events(); event.Active {
			t.Fatalf("reported client did not stop: %+v", event)
		}
	}
}

func TestDispatcharrMissingAndInvalidNonLiveClients(t *testing.T) {
	const empty = `{"live":{"channels":[]},"vod":{"total_connections":0,"vod_connections":[]},"catchup":{"total_connections":0,"timeshift_sessions":[]}}`
	for _, tc := range []struct {
		name, body string
		want       int
		invalid    bool
	}{
		{"VOD aggregate without clients", strings.Replace(empty, `"vod":{"total_connections":0`, `"vod":{"total_connections":1`, 1), 1, false},
		{"catch-up aggregate without clients", strings.Replace(empty, `"catchup":{"total_connections":0`, `"catchup":{"total_connections":1`, 1), 1, false},
		{"VOD detail missing clients", strings.Replace(empty, `"vod_connections":[]`, `"vod_connections":[{"content_type":"episode","content_uuid":"e","connection_count":1}]`, 1), 1, false},
		{"catch-up detail missing clients", strings.Replace(empty, `"timeshift_sessions":[]`, `"timeshift_sessions":[{"session_id":"s","connection_count":1}]`, 1), 1, false},
		{"VOD missing identity", strings.Replace(empty, `"vod_connections":[]`, `"vod_connections":[{"connection_count":1}]`, 1), 0, true},
		{"VOD duplicate identity", strings.Replace(empty, `"vod_connections":[]`, `"vod_connections":[{"content_type":"movie","content_uuid":"m","connection_count":2,"connections":[{"client_id":"x"},{"client_id":"x"}]}]`, 1), 0, true},
		{"catch-up missing client identity", strings.Replace(empty, `"timeshift_sessions":[]`, `"timeshift_sessions":[{"session_id":"s","connection_count":1,"connections":[{}]}]`, 1), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer srv.Close()
			sessions, err := NewDispatcharrWatcher(srv.URL, "key", true).Snapshot(context.Background())
			if (err != nil) != tc.invalid || (!tc.invalid && len(sessions) != tc.want) {
				t.Fatalf("snapshot = %+v, %v", sessions, err)
			}
		})
	}
}
