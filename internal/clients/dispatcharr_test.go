package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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
		if r.URL.Path != "/prefix/proxy/ts/status" || r.Header.Get("X-API-Key") != "secret" || r.URL.RawQuery != "" {
			t.Errorf("wrong request path or authentication: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"channels": channels, "count": len(channels)})
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
		_ = json.NewEncoder(w).Encode(map[string]any{"channels": []dispatcharrChannel{{ID: "channel", ClientCount: 11, Clients: clients[:10]}}})
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
		{"null list", `{"channels":null}`, 200},
		{"missing channel ID", `{"channels":[{"client_count":1}]}`, 200},
		{"missing client ID", `{"channels":[{"channel_id":"c","client_count":1,"clients":[{}]}]}`, 200},
		{"duplicate client", `{"channels":[{"channel_id":"c","client_count":2,"clients":[{"client_id":"a"},{"client_id":"a"}]}]}`, 200},
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
	body.Store(`{"channels":[]}`)
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
	body.Store(playing)
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
	body.Store(`{"channels":[]}`)
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
