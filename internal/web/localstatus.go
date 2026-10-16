package web

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
)

// EnableLocalStatus serves GET /local/status, without a sign-in, to
// requests from this machine only. The Windows tray icon reads it to show
// what AutoThrottle is doing; nothing else turns it on.
func (s *Server) EnableLocalStatus() { s.localStatus = true }

// localStatus is the Dashboard's headline and little more: whether
// anything is throttled, never the configuration behind it.
type localStatus struct {
	Setup     bool   `json:"setup"` // no account yet
	Statement string `json:"statement"`
	Paused    bool   `json:"paused"`
	Sessions  int    `json:"sessions"`
	Throttled int    `json:"throttled"`
}

func (s *Server) localStatusHandler(w http.ResponseWriter, r *http.Request) {
	if !fromThisMachine(r) {
		s.notFound(w, r)
		return
	}
	status := localStatus{Setup: s.auth.NeedsSetup()}
	if status.Setup {
		status.Statement = "Not set up yet"
	} else {
		data, err := s.buildDashboardData(r)
		if err != nil {
			slog.Error("building the local status", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		status.Statement = data.Hero.Statement
		status.Paused = data.Paused
		status.Sessions = data.ActiveSessions
		status.Throttled = data.ThrottledCount
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(status)
}

// fromThisMachine is a loopback connection that no proxy forwarded: a
// reverse proxy on the same machine also connects from loopback, but on
// someone else's behalf.
func fromThisMachine(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
