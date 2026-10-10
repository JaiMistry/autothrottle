package web

import (
	"regexp"
	"strings"
	"testing"
)

// openTag matches an element's opening tag, attributes and all.
var openTag = regexp.MustCompile(`<[a-z]+\s[^>]*>`)

// Every block that refreshes itself on a timer or the live stream
// targets itself, and anything inside that does not set its own
// target — a boosted link, a boosted form — would inherit that and load
// the next whole page into the block: a second sidebar inside the first.
// That has happened twice. hx-disinherit="*" on the block prevents it.
func TestSelfRefreshingBlocksDoNotLendTheirTarget(t *testing.T) {
	app := newApp(t)
	app.mediaServer("JF", "jellyfin", "http://jf.lan")
	app.downloader("qBit")
	app.events(3, "info")
	app.post("/pause-toggle", nil) // so the banner is drawn too

	seen := 0
	for _, path := range []string{"/", "/configuration?tab=connections", "/configuration?tab=mappings", "/logs"} {
		for _, tag := range openTag.FindAllString(app.get(path).Body.String(), -1) {
			refreshes := strings.Contains(tag, "every ") || strings.Contains(tag, `"live`)
			if !strings.Contains(tag, "hx-trigger=") || !refreshes {
				continue
			}
			seen++
			if !strings.Contains(tag, `hx-disinherit="*"`) {
				t.Errorf("%s: a self-refreshing block lends its target to what is inside it:\n%s", path, tag)
			}
		}
	}
	if seen < 4 {
		t.Errorf("found %d self-refreshing blocks; the check is not looking at them", seen)
	}
}
