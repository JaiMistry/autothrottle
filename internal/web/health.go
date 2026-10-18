package web

import (
	"io"
	"net/http"
)

// health answers Docker's health check: the app is up and serving. It
// says nothing of the media servers or download clients, whose outages
// show on the Connections page: an AutoThrottle marked unhealthy for
// qBittorrent restarting would be restarted for someone else's outage.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}
