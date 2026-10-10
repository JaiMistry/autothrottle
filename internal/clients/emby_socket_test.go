package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// embyFakeServer is an Emby/Jellyfin notification socket the test drives.
type embyFakeServer struct {
	subscribed chan string
	keepAlives chan string
	query      chan string
	auth       chan string
	push       chan string
}

func newEmbyFakeServer(t *testing.T) (*embyFakeServer, *httptest.Server) {
	t.Helper()
	f := &embyFakeServer{
		subscribed: make(chan string, 1),
		keepAlives: make(chan string, 4),
		query:      make(chan string, 1),
		auth:       make(chan string, 1),
		push:       make(chan string, 4),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()

		select {
		case f.query <- r.URL.RawQuery:
		default:
		}
		select {
		case f.auth <- r.Header.Get("Authorization"):
		default:
		}

		// The subscribe first, then keepalives.
		go func() {
			first := true
			for {
				_, data, err := c.Read(ctx)
				if err != nil {
					return
				}
				if first {
					first = false
					select {
					case f.subscribed <- string(data):
					default:
					}
					continue
				}
				select {
				case f.keepAlives <- string(data):
				default:
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-f.push:
				if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func TestEmbyWatcherSocketRoundTrip(t *testing.T) {
	fake, srv := newEmbyFakeServer(t)

	w := newEmbyWatcher(flavorJellyfin, srv.URL, "sekrit", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.connectAndListen(ctx) }()

	// Jellyfin 12 refuses a key in the URL: it goes in the header, as on
	// the REST calls.
	select {
	case q := <-fake.query:
		if strings.Contains(q, "sekrit") {
			t.Errorf("handshake query = %q, want no key in it", q)
		}
		if !strings.Contains(q, "deviceId=autothrottle") {
			t.Errorf("handshake query = %q, want it to carry a device id", q)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the handshake")
	}
	if auth := <-fake.auth; !strings.Contains(auth, `Token="sekrit"`) {
		t.Errorf("handshake Authorization = %q, want the key", auth)
	}

	// Nothing is pushed until the client subscribes.
	select {
	case msg := <-fake.subscribed:
		if !strings.Contains(msg, `"MessageType":"SessionsStart"`) {
			t.Errorf("first client message = %s, want a SessionsStart subscription", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client never subscribed; no sessions would ever arrive")
	}

	// A short keepalive, so the test does not sit for 15s.
	fake.push <- `{"MessageType":"ForceKeepAlive","Data":2}`

	fake.push <- `{"MessageType":"Sessions","Data":[
		{"Id":"remote","RemoteEndPoint":"8.8.8.8","NowPlayingItem":{"Name":"A"},"PlayState":{"IsPaused":false}},
		{"Id":"lan","RemoteEndPoint":"192.168.1.5","NowPlayingItem":{"Name":"B"},"PlayState":{"IsPaused":false}}
	]}`

	// Exemption is on, so only the remote session should surface.
	assertEvent(t, w, "remote", true)
	assertNoEvent(t, w)

	// Dropping it from the list is how a stop is reported.
	fake.push <- `{"MessageType":"Sessions","Data":[]}`
	assertEvent(t, w, "remote", false)

	// The server drops a client that goes quiet.
	select {
	case msg := <-fake.keepAlives:
		if !strings.Contains(msg, `"MessageType":"KeepAlive"`) {
			t.Errorf("expected a KeepAlive, got %s", msg)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("client never sent a KeepAlive; the server would drop the connection")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("connectAndListen did not return after cancellation")
	}
}

// Message types of no interest must not break the read loop.
func TestEmbyWatcherIgnoresUnknownMessages(t *testing.T) {
	fake, srv := newEmbyFakeServer(t)

	w := newEmbyWatcher(flavorEmby, srv.URL, "k", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.connectAndListen(ctx) }()

	<-fake.subscribed

	fake.push <- `{"MessageType":"UserDataChanged","Data":{"UserId":"x"}}`
	fake.push <- `{"MessageType":"ScheduledTasksInfo","Data":[]}`
	fake.push <- `not json at all`
	fake.push <- `{"MessageType":"Sessions","Data":[{"Id":"a","RemoteEndPoint":"8.8.8.8","NowPlayingItem":{"Name":"A"},"PlayState":{"IsPaused":false}}]}`

	assertEvent(t, w, "a", true)
}

// A malformed Sessions payload is not "the list is now empty".
func TestEmbyWatcherIgnoresMalformedSessionList(t *testing.T) {
	fake, srv := newEmbyFakeServer(t)

	w := newEmbyWatcher(flavorEmby, srv.URL, "k", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.connectAndListen(ctx) }()

	<-fake.subscribed

	fake.push <- `{"MessageType":"Sessions","Data":[{"Id":"a","RemoteEndPoint":"8.8.8.8","NowPlayingItem":{"Name":"A"},"PlayState":{"IsPaused":false}}]}`
	assertEvent(t, w, "a", true)

	fake.push <- `{"MessageType":"Sessions","Data":"suddenly a string"}`
	assertNoEvent(t, w)

	w.mu.Lock()
	_, still := w.active["a"]
	w.mu.Unlock()
	if !still {
		t.Error("a malformed push must leave the tracked session set alone")
	}
}
