package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// idTimeout bounds the lookups of what each media server reports as its
// ID. One that doesn't answer in time is never a duplicate.
const idTimeout = 3 * time.Second

// sameEndpoint reports whether two addresses point at the same service,
// ignoring case, a trailing slash and default ports. Unparseable input
// falls back to a trimmed string comparison.
func sameEndpoint(a, b string) bool {
	norm := func(raw string) string {
		raw = strings.TrimSpace(raw)
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return strings.ToLower(strings.TrimRight(raw, "/"))
		}
		host := strings.ToLower(parsed.Host)
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

// sameServerAs returns which of others reports the same ID as the media
// server at rawURL, or -1. All are asked at once.
func sameServerAs(ctx context.Context, msType, rawURL string, others []db.MediaServer) int {
	ctx, cancel := context.WithTimeout(ctx, idTimeout)
	defer cancel()

	var target string
	ids := make([]string, len(others))
	var wg sync.WaitGroup
	wg.Go(func() { target, _ = clients.MediaServerID(ctx, msType, rawURL) })
	for i, ms := range others {
		wg.Go(func() { ids[i], _ = clients.MediaServerID(ctx, ms.Type, ms.URL) })
	}
	wg.Wait()

	if target == "" {
		return -1
	}
	for i, id := range ids {
		if id == target {
			return i
		}
	}
	return -1
}

// duplicateMediaServer returns the message to show when this name or
// address is already configured, or "" when it is not. skipID is the
// row being edited, which must not count as a clash with itself.
func (s *Server) duplicateMediaServer(ctx context.Context, skipID int64, msType, name, rawURL string) string {
	servers, err := s.db.ListMediaServers()
	if err != nil {
		return "" // the save itself will fail and report properly
	}
	others := servers[:0]
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
		others = append(others, ms)
	}

	// The same server under another address: an IP and a hostname, say.
	// Not by where the addresses resolve: behind one reverse proxy, every
	// service shares an address.
	if i := sameServerAs(ctx, msType, rawURL, others); i >= 0 {
		return fmt.Sprintf("This is the same server as %q, added at %s. Edit that one instead of adding it twice.", others[i].Name, others[i].URL)
	}
	return ""
}

// duplicateDownloader is the same check for download clients, two of
// which on one client would fight over its rate limits. They report no
// ID of their own, so one under two addresses can't be told.
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
