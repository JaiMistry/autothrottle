package web

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// EnableLocalStatus serves GET /local/status and POST /local/throttling,
// without a sign-in, to requests from this machine only. The Windows tray
// icon reads the one to show what AutoThrottle is doing, and its menu
// turns throttling off and on with the other; nothing else turns them on.
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

// localThrottlingHandler turns throttling off (paused=true) or back on
// (paused=false). It sets rather than toggles, so a menu showing a stale
// state can't do the opposite of what it says. Browsers never get it:
// they send Origin or Sec-Fetch-Site with every POST and the tray sends
// neither, so no web page can switch throttling by posting a form here.
func (s *Server) localThrottlingHandler(w http.ResponseWriter, r *http.Request) {
	if !fromThisMachine(r) || r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		s.notFound(w, r)
		return
	}
	if s.auth.NeedsSetup() {
		http.Error(w, "not set up yet", http.StatusConflict)
		return
	}
	paused, err := strconv.ParseBool(r.PostFormValue("paused"))
	if err != nil {
		http.Error(w, "paused must be true or false", http.StatusBadRequest)
		return
	}
	s.scheduler.SetPaused(r.Context(), paused)
	w.WriteHeader(http.StatusNoContent)
}

// fromThisMachine is a loopback connection, addressed to this machine,
// that no proxy forwarded. A reverse proxy on the same machine connects
// from loopback too, on someone else's behalf, and a web page that points
// its own hostname at 127.0.0.1 (DNS rebinding) reaches loopback with that
// hostname still in Host.
func fromThisMachine(r *http.Request) bool {
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	addr, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(addr); ip == nil || !ip.IsLoopback() {
		return false
	}
	name := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		name = h
	}
	switch strings.ToLower(name) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}
