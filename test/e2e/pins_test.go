package e2e

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The pinned round tests the oldest version of each server the README
// supports (compose.yml says why). These keep the two in step: a README
// minimum that moves without its pin, or a pin that moves on its own,
// fails here. Choosing the exact build stays a person's call: the first
// stable one in the series, which may not be the first image.
var pinnedServers = []struct{ name, image string }{
	{"Plex", "plexinc/pms-docker"},
	{"Jellyfin", "jellyfin/jellyfin"},
	{"Emby", "emby/embyserver"},
	{"qBittorrent", "ghcr.io/linuxserver/qbittorrent"},
	{"Transmission", "ghcr.io/linuxserver/transmission"},
	{"Deluge", "ghcr.io/linuxserver/deluge"},
}

func TestPinsAreTheREADMEMinimums(t *testing.T) {
	readme := readFile(t, "../../README.md")
	compose := readFile(t, "compose.yml")
	for _, s := range pinnedServers {
		minimum, ok := readmeMinimum(readme, s.name)
		if !ok {
			t.Errorf("the README has no supported-versions row for %s: a table row naming it, with a version like 4.9+", s.name)
			continue
		}
		tag, ok := pinnedTag(compose, s.image)
		if !ok {
			t.Errorf("compose.yml pins no %s image", s.image)
			continue
		}
		if !strings.HasPrefix(tag, minimum+".") {
			t.Errorf("the README says %s %s+, but compose.yml pins %s:%s. Pin %s's first stable %s release.",
				s.name, minimum, s.image, tag, s.name, minimum)
		}
	}
}

var (
	minimumCell = regexp.MustCompile(`^(\d+(?:\.\d+)*)\+$`)
	markup      = regexp.MustCompile(`<[^>]*>|\[|\]\([^)]*\)`)
)

// readmeMinimum finds the table row naming a server, wherever it sits,
// and returns the version it says is the oldest supported ("4.9" of "4.9+").
func readmeMinimum(readme, name string) (string, bool) {
	for _, line := range strings.Split(readme, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		named, minimum := false, ""
		for _, cell := range strings.Split(line, "|") {
			text := strings.TrimSpace(markup.ReplaceAllString(cell, ""))
			if strings.Contains(text, name) {
				named = true
			}
			if m := minimumCell.FindStringSubmatch(text); m != nil {
				minimum = m[1]
			}
		}
		if named && minimum != "" {
			return minimum, true
		}
	}
	return "", false
}

func pinnedTag(compose, image string) (string, bool) {
	m := regexp.MustCompile(`image:\s*` + regexp.QuoteMeta(image) + `:(\S+)`).FindStringSubmatch(compose)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
