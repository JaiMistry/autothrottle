package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/JaiMistry/autothrottle/internal/live"
)

// streamHeartbeat keeps an idle stream from being cut by a proxy that
// closes quiet connections, and finds a closed tab within that time.
const streamHeartbeat = 25 * time.Second

// streamLifetime ends every stream after a while; the browser opens a
// new one at once. A stream outliving its session would otherwise keep
// a signed-out tab subscribed, so each one is made to pass the login
// check again.
const streamLifetime = 10 * time.Minute

// Live is the hub open pages subscribe to, for the parts that change
// state to announce through.
func (s *Server) Live() *live.Hub { return s.live }

// liveEvents is a Server-Sent Events stream naming what changed, one
// line per topic. It carries no content: the page fetches its own
// fragment, the same one a timed refresh would.
func (s *Server) liveEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// The server's write timeout is for ordinary responses; a stream
	// would be cut off mid-flight by it.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// nginx buffers responses unless told not to, which would hold
	// every change back until the buffer filled.
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
