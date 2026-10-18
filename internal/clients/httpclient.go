package clients

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// newHTTPClient is the client for every request that carries a token or a
// password. A timeout of 0 is none, for a WebSocket that stays open.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: followSameServer}
}

// followSameServer follows a redirect only to the server the request was
// made to. Go drops Authorization and Cookie when a redirect leaves the
// host, but not X-Plex-Token or X-Emby-Token, and a 307 or 308 sends a
// login form's password again: a redirect elsewhere, such as to a sign-in
// page in front of the server, would hand them to whoever it points at.
// Moving from https to http would send them unencrypted, so that is
// refused too.
func followSameServer(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	from := via[0].URL
	if !strings.EqualFold(req.URL.Host, from.Host) || (secure(from) && !secure(req.URL)) {
		return fmt.Errorf("redirected to another address, %s://%s, which was not followed", req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// secure covers a WebSocket's first request, whose scheme is still wss.
func secure(u *url.URL) bool {
	return u.Scheme == "https" || u.Scheme == "wss"
}
