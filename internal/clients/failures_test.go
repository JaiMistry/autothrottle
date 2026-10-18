package clients

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// unreachable has nothing listening; badURL cannot even be requested.
const (
	unreachable = "http://127.0.0.1:1"
	badURL      = "http://bad host"
)

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func wantErr(t *testing.T, what string, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Errorf("%s: %v, want an error containing %q", what, err, contains)
	}
}

// Every way a download client can fail is reported, not swallowed: the
// Dashboard shows a failed apply as Unreachable, never as the old rate.
func TestQBittorrentFailures(t *testing.T) {
	ctx := context.Background()
	status := func(code int) string {
		return serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
	}
	wantErr(t, "test, server error", TestQBittorrent(ctx, status(500), "", ""), "unexpected status 500")
	wantErr(t, "test, bad address", TestQBittorrent(ctx, badURL, "", ""), "building request")
	wantErr(t, "test, unreachable", TestQBittorrent(ctx, unreachable, "", ""), "connecting to")
	wantErr(t, "login, bad address", TestQBittorrent(ctx, badURL, "u", "p"), "building request")
	wantErr(t, "login, unreachable", TestQBittorrent(ctx, unreachable, "u", "p"), "connecting to")

	wantErr(t, "limits, login refused", SetQBittorrentLimits(ctx, status(500), "u", "p", 1, 1), "unexpected status 500")
	wantErr(t, "limits, bad address", SetQBittorrentLimits(ctx, badURL, "", "", 1, 1), "building request")
	wantErr(t, "limits, unreachable", SetQBittorrentLimits(ctx, unreachable, "", "", 1, 1), "connecting to")
	uploadFails := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "setUploadLimit") {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	wantErr(t, "limits, upload refused", SetQBittorrentLimits(ctx, uploadFails, "", "", 1, 1), "setting upload limit")
	if err := (QBittorrent{}).SetLimits(ctx, serve(t, func(http.ResponseWriter, *http.Request) {}), "", "", 1, 1); err != nil {
		t.Errorf("limits without a login: %v", err)
	}
}

func TestTransmissionFailures(t *testing.T) {
	ctx := context.Background()
	wantErr(t, "bad address", TestDownloader(ctx, "transmission", badURL, "", ""), "building request")
	wantErr(t, "unreachable", TestTransmission(ctx, unreachable, "", ""), "connecting to")
	garbled := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "not json") })
	wantErr(t, "unreadable reply", TestTransmission(ctx, garbled, "", ""), "decoding response")
	rejected := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"result":"no permission"}`) })
	wantErr(t, "limits rejected", (Transmission{}).SetLimits(ctx, rejected, "", "", 1, 1), "setting speed limits")
	wantErr(t, "unknown type", TestDownloader(ctx, "rtorrent", unreachable, "", ""), "unsupported downloader type")
}

func TestEmbyLabels(t *testing.T) {
	season, ep := 2, 5
	cases := []struct {
		item          *embyNowPlaying
		label         string
		durationTicks time.Duration
	}{
		{nil, "", 0},
		{&embyNowPlaying{Name: "Heat", RunTimeTicks: 600}, "Heat", 60 * time.Microsecond},
		{&embyNowPlaying{Name: "Ep", SeriesName: "Andor", SeasonNumber: &season, Episode: &ep}, "Andor S2E5", 0},
		{&embyNowPlaying{Name: "Ep", SeriesName: "Andor"}, "Andor", 0},
	}
	for _, c := range cases {
		s := embySession{NowPlayingItem: c.item}
		if s.label() != c.label || s.duration() != c.durationTicks {
			t.Errorf("%+v: label %q, duration %v", c.item, s.label(), s.duration())
		}
	}
	wantErr(t, "bad address", TestEmby(context.Background(), badURL, "k"), "building request")
}

// A server that accepts the subscription but never sends sessions is
// logged, and its keepalive still runs, at no more than one a second.
func TestEmbySilentSubscriptionAndKeepAlive(t *testing.T) {
	grace := embySubscriptionGrace
	embySubscriptionGrace = 50 * time.Millisecond
	t.Cleanup(func() { embySubscriptionGrace = grace })

	fake, srv := newEmbyFakeServer(t)
	w := NewJellyfinWatcher(srv.URL, "k", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	<-fake.subscribed
	if !w.Connection().Connected {
		t.Error("not connected after subscribing")
	}
	// A tiny timeout is floored at a second. The keepalive starts once,
	// so a later one that cannot be read (which would mean 30s) is moot.
	fake.push <- `{"MessageType":"ForceKeepAlive","Data":1}`
	fake.push <- `{"MessageType":"ForceKeepAlive","Data":"soon"}`
	select {
	case <-fake.keepAlives:
	case <-time.After(3 * time.Second):
		t.Fatal("no keepalive")
	}
}

// A session without an id is not a session.
func TestEmbySessionWithoutIDIsIgnored(t *testing.T) {
	w := NewEmbyWatcher(unreachable, "k", false)
	if got := w.activeSet([]embySession{{ID: ""}}); len(got) != 0 {
		t.Errorf("active %+v", got)
	}
}

// A login reply cut off by a dropped connection is a failure, not a
// session.
func TestQBittorrentTruncatedLoginReply(t *testing.T) {
	addr := serve(t, func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nOk")
		_ = buf.Flush()
		_ = conn.Close()
	})
	wantErr(t, "truncated reply", TestQBittorrent(context.Background(), addr, "u", "p"), "reading response")
}

// failingStore refuses every write, as a full or read-only disk would.
type failingStore struct{}

func (failingStore) Load() (map[string]PausedSession, error) { return nil, errors.New("disk") }
func (failingStore) Save(string, PausedSession) error        { return errors.New("disk") }
func (failingStore) Delete(string) error                     { return errors.New("disk") }
func (failingStore) Retain(map[string]bool) error            { return errors.New("disk") }

// The pause clocks still work in memory when they cannot be saved.
func TestPausedTrackerSurvivesAFailingStore(t *testing.T) {
	p := tracker(failingStore{}, time.Hour)
	now := time.Now()
	if !p.counts("a", true, time.Minute, now) || !p.counts("a", false, 2*time.Minute, now) {
		t.Error("a store failure stopped a session counting")
	}
	p.retain(map[string]bool{})
	if len(p.since) != 0 {
		t.Error("retain kept a session that is gone")
	}
}

func TestBackoffDoublesUpToTheCap(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		time.Second: 2 * time.Second, 16 * time.Second: reconnectMaxBackoff, reconnectMaxBackoff: reconnectMaxBackoff,
	} {
		if got := nextBackoff(in); got != want {
			t.Errorf("nextBackoff(%v) = %v, want %v", in, got, want)
		}
	}
}

// A connection that stayed up a while is not penalised for one drop: the
// next attempt comes after a second, not after the backoff it had built.
func TestLongConnectionResetsBackoff(t *testing.T) {
	var tracker connectionTracker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := make(chan time.Time, 4)
	go runWithBackoff(ctx, "test", unreachable, &tracker, func(context.Context) error {
		attempts <- time.Now()
		tracker.connected()
		tracker.mu.Lock()
		tracker.since = time.Now().Add(-time.Hour)
		tracker.mu.Unlock()
		return errors.New("dropped")
	})
	first := <-attempts
	if gap := (<-attempts).Sub(first); gap > 1500*time.Millisecond {
		t.Errorf("waited %v after a long-lived connection dropped", gap)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	runWithBackoff(cancelled, "test", unreachable, &tracker, func(context.Context) error {
		t.Error("connected after shutdown")
		return nil
	})
}

// Shutting down never blocks on a full event channel.
func TestEmitGivesUpOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	full := make(chan SessionEvent)
	emit(ctx, full, Session{Key: "a"}, true)
	emitDetail(ctx, full, Session{Key: "a"})
	wantErr(t, "bad address", getJSON(ctx, badURL, func(*http.Request) {}, nil), "building request")
}

func TestPlexEdgeCases(t *testing.T) {
	server := newFakePlex(t)
	server.rootStatus = http.StatusOK
	fakePlexTV(t, func(w http.ResponseWriter, r *http.Request) {})
	plexAccountURL = badURL
	wantErr(t, "bad plex.tv address", TestPlex(context.Background(), server.URL, "t"), "building token check")

	// The follow-up lookup finds its own session among others.
	server.set(episode("other", "playing", false), episode("1", "playing", false))
	w := NewPlexWatcher(server.URL, "t", false)
	w.fillDetail(context.Background(), "1")
	if e := nextEvent(t, w); e.Key != "1" || e.Title != "Andor S1E4" {
		t.Errorf("detail %+v", e)
	}

	// Only the poll can see a pause go stale: the socket goes quiet.
	server.set(episode("1", "paused", false))
	w.paused = newPausedTracker(10 * time.Millisecond)
	if got, _ := w.Snapshot(context.Background()); len(got) != 1 {
		t.Fatalf("fresh pause: %+v", got)
	}
	time.Sleep(20 * time.Millisecond)
	if got, _ := w.Snapshot(context.Background()); len(got) != 0 {
		t.Errorf("stale pause still counts: %+v", got)
	}
}
