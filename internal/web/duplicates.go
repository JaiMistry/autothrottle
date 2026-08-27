package web

import (
	"fmt"
	"net/url"
	"strings"
)

// sameEndpoint reports whether two addresses point at the same service.
//
// Compared on scheme, host and path rather than as raw strings, so the
// trailing slash and the capitalisation someone happens to type do not
// disguise a server that is already configured. Anything that will not
// parse falls back to a trimmed string comparison — being wrong here
// only costs a duplicate warning that does not fire.
func sameEndpoint(a, b string) bool {
	norm := func(raw string) string {
		raw = strings.TrimSpace(raw)
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return strings.ToLower(strings.TrimRight(raw, "/"))
		}
		host := strings.ToLower(parsed.Host)
		// Default ports are implied, so :80 and :443 must not make an
		// address look different from the same one without them.
		switch {
		case parsed.Scheme == "http" && strings.HasSuffix(host, ":80"):
			host = strings.TrimSuffix(host, ":80")
		case parsed.Scheme == "https" && strings.HasSuffix(host, ":443"):
			host = strings.TrimSuffix(host, ":443")
		}
		return strings.ToLower(parsed.Scheme) + "://" + host + strings.TrimRight(parsed.Path, "/")
	}
	return norm(a) == norm(b)
}

func sameName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// duplicateMediaServer returns the message to show when this name or
// address is already configured, or "" when it is not. skipID is the
// row being edited, which must not count as a clash with itself.
func (s *Server) duplicateMediaServer(skipID int64, name, rawURL string) string {
	servers, err := s.db.ListMediaServers()
	if err != nil {
		return "" // the save itself will fail and report properly
	}
	for _, ms := range servers {
		if ms.ID == skipID {
			continue
		}
		if sameName(ms.Name, name) {
			return fmt.Sprintf("A media server called %q already exists. Give this one a different name.", strings.TrimSpace(name))
		}
		if sameEndpoint(ms.URL, rawURL) {
			return fmt.Sprintf("%s is already added as %q. Edit that one instead of adding it twice.", ms.URL, ms.Name)
		}
	}
	return ""
}

// duplicateDownloader is the same check for download clients. Two
// entries pointed at one client would fight over its rate limits, each
// resolving throttle state from its own mappings.
func (s *Server) duplicateDownloader(skipID int64, name, rawURL string) string {
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		return ""
	}
	for _, dl := range downloaders {
		if dl.ID == skipID {
			continue
		}
		if sameName(dl.Name, name) {
			return fmt.Sprintf("A downloader called %q already exists. Give this one a different name.", strings.TrimSpace(name))
		}
		if sameEndpoint(dl.URL, rawURL) {
			return fmt.Sprintf("%s is already added as %q. Edit that one instead of adding it twice.", dl.URL, dl.Name)
		}
	}
	return ""
}
