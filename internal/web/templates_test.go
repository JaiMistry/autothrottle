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

// Parsing is not enough: html/template resolves its contextual escaping
// at execution, so a tag left unclosed by an edit parses cleanly and
// then fails on the request. Rendering each page with representative
// data catches that here instead of in the browser.
func TestPagesRender(t *testing.T) {
	funcs := templateFuncs()
	cases := map[string]any{
		"login": pageData{Title: "Log in", Version: "test", Asset: func(p string) string { return p }},
		"dashboard": dashboardData{
			pageData: pageData{Title: "Dashboard", Authenticated: true, Version: "test", Asset: func(p string) string { return p }},
			Hero:     heroState{Statement: "Nothing is throttled", Reason: "…", Tone: "good"},
			Overall:  overallFlag{Tone: "good", Label: "Idle", Icon: "normal"},
			Servers:  []serverRowView{{Name: "Plex", Type: "plex", State: "playing", Sessions: 2, Detail: "…"}},
			Downloaders: []downloaderRowView{
				{Name: "qBittorrent", Type: "qbittorrent", State: "throttled", Cause: "because Plex is playing", Down: 2200, Up: 512},
			},
			Activity: newPagedLog("Recent activity", "activity-card", "/activity",
				[]logRowView{{Time: "12:00:00", Level: "info", Message: "hello", Downloader: "qBittorrent"}},
				1, 10, 1, nil),
		},
		"logs": logsData{
			pageData: pageData{Title: "Logs", Authenticated: true, Version: "test", Asset: func(p string) string { return p }},
			Counts:   map[string]int{"info": 1, "warn": 0, "error": 1},
			CountAll: 2,
			Matching: 2,
			Log: newPagedLog("Events", "log-card", "/logs/rows",
				[]logRowView{{Time: "12:00:00", Level: "error", Message: "boom"}}, 2, 20, 1, nil),
		},
		"configuration": configPageData{
			pageData:     pageData{Title: "Configuration", Authenticated: true, Version: "test", Asset: func(p string) string { return p }},
			Tab:          "media-servers",
			MediaServers: []mediaServerView{{ID: 1, Name: "Plex", Type: "plex", URL: "http://x", Enabled: true, Sessions: 2}},
			Downloaders:  []downloaderView{{ID: 1, Name: "qb", Type: "qbittorrent", URL: "http://y", Enabled: true, State: "normal"}},
			MappingOn:    map[string]bool{"1:1": true},
			About:        aboutView{Commit: "a3f9c21", BuiltNote: "built 12 Aug 2026", Runtime: "go1.26 · linux/amd64 · docker", BootNote: "last restart: 16 Aug 2026, 09:41", BootUnix: 1755337260000},
			Uptime:       "0d : 00h : 00m : 00s",
			AuthSummary:  "enabled · signed in as admin",
			Update:       idleUpdate(),
		},
		"analytics": analyticsData{
			pageData:   pageData{Title: "Analytics", Authenticated: true, Version: "test", Asset: func(p string) string { return p }},
			RangeKey:   "24h",
			HasClients: true,
			HasSamples: false,
		},
	}

	for page, data := range cases {
		tmpl, err := parsePage(page, funcs)
		if err != nil {
			t.Fatalf("parsing %s: %v", page, err)
		}
		if err := tmpl.ExecuteTemplate(discard{}, "layout", data); err != nil {
			t.Errorf("rendering %s: %v", page, err)
		}
	}
}

// Every Configuration tab is its own branch, and a broken one only
// shows when someone clicks it.
func TestConfigurationTabsRender(t *testing.T) {
	tmpl, err := parsePage("configuration", templateFuncs())
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range []string{"media-servers", "downloaders", "mappings", "about"} {
		data := configPageData{
			pageData:     pageData{Title: "Configuration", Authenticated: true, Asset: func(p string) string { return p }},
			Tab:          tab,
			MediaServers: []mediaServerView{{ID: 1, Name: "Plex", Type: "plex", URL: "http://x", Enabled: true}},
			Downloaders:  []downloaderView{{ID: 1, Name: "qb", Type: "qbittorrent", URL: "http://y", Enabled: true, State: "normal"}},
			MappingOn:    map[string]bool{"1:1": true},
			About:        aboutView{Commit: "a3f9c21", BuiltNote: "built 12 Aug 2026", Runtime: "go1.26 · linux/amd64 · docker", BootNote: "last restart: 16 Aug 2026, 09:41", BootUnix: 1755337260000},
			Uptime:       "0d : 00h : 00m : 00s",
			AuthSummary:  "enabled · signed in as admin",
			Update:       idleUpdate(),
		}
		if err := tmpl.ExecuteTemplate(discard{}, "layout", data); err != nil {
			t.Errorf("rendering tab %s: %v", tab, err)
		}
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// The Logs page has three mutually exclusive bodies and each is a
// separate branch: no events at all, none matching the filter, and the
// paged card. A broken one only shows when someone reaches that state.
func TestLogsEmptyStatesRender(t *testing.T) {
	tmpl, err := parsePage("logs", templateFuncs())
	if err != nil {
		t.Fatal(err)
	}
	base := logsData{
		pageData: pageData{Title: "Logs", Authenticated: true, Asset: func(p string) string { return p }},
		Counts:   map[string]int{"info": 0, "warn": 0, "error": 0},
		Log:      newPagedLog("Events", "log-card", "/logs/rows", nil, 0, 20, 1, nil),
	}
	noEvents := base
	noEvents.NoEvents = true

	noMatch := base
	noMatch.Search = "nothing"
	noMatch.Level = "warn"

	for name, data := range map[string]logsData{"no events": noEvents, "no match": noMatch} {
		if err := tmpl.ExecuteTemplate(discard{}, "layout", data); err != nil {
			t.Errorf("rendering %q: %v", name, err)
		}
	}
}

// Paging arithmetic is shared by two pages that disagree about page
// size, so it is worth pinning on its own.
func TestPagedLogArithmetic(t *testing.T) {
	rows := make([]logRowView, 10)
	p := newPagedLog("Events", "t", "/e", rows, 34, 10, 2, nil)
	if p.Pages != 4 {
		t.Errorf("pages = %d, want 4", p.Pages)
	}
	if p.RangeFrom != 11 || p.RangeTo != 20 {
		t.Errorf("range = %d–%d, want 11–20", p.RangeFrom, p.RangeTo)
	}
	if !p.HasPrev || !p.HasNext {
		t.Error("page 2 of 4 has both neighbours")
	}

	// A page past the end clamps rather than showing an empty card.
	if got := newPagedLog("Events", "t", "/e", rows, 34, 10, 99, nil); got.Page != 4 {
		t.Errorf("page clamped to %d, want 4", got.Page)
	}
	// No events at all still renders a sane pager.
	empty := newPagedLog("Events", "t", "/e", nil, 0, 10, 1, nil)
	if empty.Pages != 1 || empty.HasNext || empty.RangeFrom != 0 {
		t.Errorf("empty pager = %+v", empty)
	}
}
