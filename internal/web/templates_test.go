package web

import (
	"strings"
	"testing"
)

// Templates are parsed at construction, not at build, so a missing
// function or a malformed action is invisible to `go build` and only
// surfaces when the process starts. This catches that class of error in
// the test suite instead of in a crash loop.
func TestTemplatesParse(t *testing.T) {
	funcs := templateFuncs()
	for _, page := range pages {
		if _, err := parsePage(page, funcs); err != nil {
			t.Errorf("parsing %s: %v", page, err)
		}
	}
}

// Every partial the pages call has to exist. A missing one is a runtime
// error on the request that reaches it, which for a rarely-hit branch
// can survive a manual click-through.
func TestPagesDefineContent(t *testing.T) {
	funcs := templateFuncs()
	for _, page := range pages {
		tmpl, err := parsePage(page, funcs)
		if err != nil {
			t.Fatalf("parsing %s: %v", page, err)
		}
		var found bool
		for _, defined := range tmpl.Templates() {
			if defined.Name() == "content" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not define a \"content\" template", page)
		}
	}
}

func TestRateLabelNeverRendersABareZero(t *testing.T) {
	// A bare 0 in a rate field reads as "stopped" rather than "no
	// ceiling", which is the opposite of what it means.
	if got := rateLabel(0); !strings.Contains(got, "unlimited") {
		t.Errorf("rateLabel(0) = %q, want it to say unlimited", got)
	}
}

func TestCommasGroupsThousands(t *testing.T) {
	cases := map[int64]string{0: "0", 512: "512", 2200: "2,200", 1048576: "1,048,576", -2200: "-2,200"}
	for in, want := range cases {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}
