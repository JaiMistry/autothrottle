package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/JaiMistry/autothrottle/internal/live"
)

// Variables so tests can shorten them.
var (
	// streamHeartbeat keeps proxies from cutting an idle stream, and finds
	// a closed tab within that time.
	streamHeartbeat = 25 * time.Second

	// streamLifetime ends every stream after a while so the browser's
	// reconnect passes the login check again; a stream must not outlive
	// its session.
	streamLifetime = 10 * time.Minute
)

// Live is the hub open pages subscribe to.
func (s *Server) Live() *live.Hub { return s.live }

// liveEvents is a Server-Sent Events stream naming what changed, one line
// per topic. It carries no content: the page fetches its own fragment.
func (s *Server) liveEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// The server's write timeout would cut the stream off mid-flight.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// Without this nginx buffers the stream and holds every change back.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub := s.live.Subscribe()
	defer sub.Close()

	// How long the browser waits before reconnecting after a drop.
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil || rc.Flush() != nil {
		return
	}

	beat := time.NewTicker(streamHeartbeat)
	defer beat.Stop()
	end := time.NewTimer(streamLifetime)
	defer end.Stop()

	for {
		var out string
		select {
		case <-r.Context().Done():
			return
		case <-sub.Done():
			return
		case <-end.C:
			return
		case <-beat.C:
			out = ": keep-alive\n\n"
		case <-sub.Wake():
			for _, t := range sub.Take() {
				out += "data: " + string(t) + "\n\n"
			}
		}
		if out == "" {
			continue
		}
		if _, err := fmt.Fprint(w, out); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
