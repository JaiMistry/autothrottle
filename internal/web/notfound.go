package web

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// The 404 page is open to anyone, so its logging must survive a path
// scan: each address once per notFoundRepeat, at most notFoundPerMinute
// a minute. The page shows its line either way.
const (
	notFoundRepeat    = 10 * time.Minute
	notFoundPerMinute = 20
	notFoundMaxPath   = 200
)

// notFoundLog decides which 404s become log events.
type notFoundLog struct {
	mu          sync.Mutex
	seen        map[string]time.Time
	windowStart time.Time
	inWindow    int
}

// unprompted are addresses browsers ask for on their own.
func unprompted(path string) bool {
	return path == "/favicon.ico" || strings.HasPrefix(path, "/apple-touch-icon")
}

// allow reports whether this 404 should be logged now.
func (l *notFoundLog) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = make(map[string]time.Time)
	}
	if last, ok := l.seen[key]; ok && now.Sub(last) < notFoundRepeat {
		return false
	}
	if now.Sub(l.windowStart) >= time.Minute {
		l.windowStart, l.inWindow = now, 0
		// Forget what is past repeating, so the map stays bounded.
		for k, t := range l.seen {
			if now.Sub(t) >= notFoundRepeat {
				delete(l.seen, k)
			}
		}
	}
	if l.inWindow >= notFoundPerMinute {
		return false
	}
	l.inWindow++
	l.seen[key] = now
	return true
}

// notFoundMessage is the line the page shows and the log records.
func notFoundMessage(method, path string) string {
	if utf8.RuneCountInString(path) > notFoundMaxPath {
		path = string([]rune(path)[:notFoundMaxPath]) + "…"
	}
	return "no route for " + method + " " + path
}

// recordNotFound writes the 404 to the log, as the page says it did.
func (s *Server) recordNotFound(r *http.Request, message string, at time.Time) {
	if unprompted(r.URL.Path) || !s.notFoundLog.allow(r.Method+" "+r.URL.Path, at) {
		return
	}
	if err := s.db.InsertEvent(db.Event{Level: "warn", Message: message, OccurredAt: db.FormatTime(at)}); err != nil {
		slog.Error("logging a 404", "error", err)
	}
}
