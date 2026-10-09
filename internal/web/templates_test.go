package web

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

func TestCommasGroupsThousands(t *testing.T) {
	cases := map[int64]string{0: "0", 512: "512", 2200: "2,200", 1048576: "1,048,576", -2200: "-2,200"}
	for in, want := range cases {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}

func testPage(title string) pageData {
	return pageData{Title: title, Authenticated: true, Version: "test", Asset: func(p string) string { return p }}
}

func testConfigPage(tab string) configPageData {
	return configPageData{
		pageData:     testPage("Configuration"),
		Tab:          tab,
		MediaServers: []mediaServerView{{ID: 1, Name: "Plex", Type: "plex", URL: "http://x", Enabled: true, Sessions: 2}},
		Downloaders:  []downloaderView{{ID: 1, Name: "qb", Type: "qbittorrent", URL: "http://y", Enabled: true, Dot: statusDot{"good", "Default rates", "Nothing mapped is playing"}}},
		MappingOn:    map[string]bool{"1:1": true},
		About:        aboutView{Commit: "a3f9c21", BuiltNote: "built 12 Aug 2026", Runtime: "go1.26 · linux/amd64 · docker", BootNote: "last restart: 16 Aug 2026, 09:41", BootUnix: 1755337260000},
		Uptime:       "0d : 00h : 00m : 00s",
		AuthSummary:  "enabled · signed in as admin",
		Update:       idleUpdate(),
	}
}

// Templates are parsed at startup and escaped at execution, so a
// missing function or an unclosed tag only fails on the request that
// reaches it. Rendering every page, and every Configuration tab, with
// representative data catches that here instead.
func TestPagesRender(t *testing.T) {
	funcs := templateFuncs()
	cases := map[string]any{
		"login": pageData{Title: "Log in", Version: "test", Asset: func(p string) string { return p }},
		"setup": setupData{pageData: pageData{Title: "Set up", Version: "test", Asset: func(p string) string { return p }}},
		"dashboard": dashboardData{
			pageData: testPage("Dashboard"),
			Hero:     heroState{Statement: "qBittorrent throttled", Tone: "warn", Since: time.Now().Add(-time.Minute)},
			Ready:    true,
			Overall:  overallFlag{Tone: "good", Label: "Idle", Icon: "normal"},
			Servers:  []serverRowView{{Name: "Plex", Type: "plex", State: "playing", Sessions: 2, Detail: "…"}},
			Downloaders: []downloaderRowView{
				{Name: "qBittorrent", Type: "qbittorrent", State: "throttled", Cause: "because Plex is playing", Down: 2200, Up: 512},
			},
			Unreachable: []unreachableView{{"Attic Jellyfin", "Media server"}},
			History: buildHistory(
				[]db.Span{{Start: time.Now().Add(-90 * time.Minute)}},
				[]db.Span{{Start: time.Now().Add(-40 * 24 * time.Hour)}},
				time.Now(), 30),
		},
		"logs": logsData{
			pageData: testPage("Logs"),
			Counts:   map[string]int{"info": 1, "warn": 0, "error": 1},
			CountAll: 2,
			Matching: 2,
			Log: newPagedLog("Events", "log-card", "/logs/rows",
				[]logRowView{{Time: "12:00:00", Level: "error", Message: "boom"}}, 2, 20, 1, nil),
		},
		"configuration": testConfigPage("connections"),
		"notfound": notFoundData{
			pageData: pageData{Title: "Page not found", Version: "test", Asset: func(p string) string { return p }},
			SignedIn: true, Time: "14:12:21", Message: "no route for GET /nope",
		},
	}

	for _, page := range pages {
		data, ok := cases[page]
		if !ok {
			t.Errorf("no render case for page %s", page)
			continue
		}
		tmpl, err := parsePage(page, funcs)
		if err != nil {
			t.Fatalf("parsing %s: %v", page, err)
		}
		if err := tmpl.ExecuteTemplate(io.Discard, "layout", data); err != nil {
			t.Errorf("rendering %s: %v", page, err)
		}
	}

	tmpl, _ := parsePage("configuration", funcs)
	for _, tab := range []string{"mappings", "about"} {
		if err := tmpl.ExecuteTemplate(io.Discard, "layout", testConfigPage(tab)); err != nil {
			t.Errorf("rendering configuration tab %s: %v", tab, err)
		}
	}
	// The drawer open on each form, adding and editing.
	for _, sheet := range []string{"server", "downloader"} {
		for _, id := range []int64{0, 1} {
			data := testConfigPage("connections")
			data.Sheet, data.EditMediaServerID, data.EditDownloaderID = sheet, id, id
			if err := tmpl.ExecuteTemplate(io.Discard, "layout", data); err != nil {
				t.Errorf("rendering the %s drawer (id %d): %v", sheet, id, err)
			}
		}
	}
}

// The Logs page has three mutually exclusive bodies and each is a
// separate branch: no events at all, none matching the filter, and the
// paged card. A broken one only shows when someone reaches that state.
func TestLogsEmptyStatesRender(t *testing.T) {
	tmpl, err := parsePage("logs", templateFuncs())
	if err != nil {
		t.Fatal(err)
	}
	base := logsData{
		pageData: testPage("Logs"),
		Counts:   map[string]int{"info": 0, "warn": 0, "error": 0},
		Log:      newPagedLog("Events", "log-card", "/logs/rows", nil, 0, 20, 1, nil),
	}
	noEvents := base
	noEvents.NoEvents = true

	noMatch := base
	noMatch.Search = "nothing"
	noMatch.Level = "warn"

	for name, data := range map[string]logsData{"no events": noEvents, "no match": noMatch} {
		if err := tmpl.ExecuteTemplate(io.Discard, "layout", data); err != nil {
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

// The healthy state is a green dot before the name; every other state
// keeps its flag after it.
func TestRowHealthyStateIsADot(t *testing.T) {
	tmpl, err := parsePage("dashboard", templateFuncs())
	if err != nil {
		t.Fatal(err)
	}
	render := func(name string, data any) string {
		var b strings.Builder
		if err := tmpl.ExecuteTemplate(&b, name, data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	dot := func(label string) string {
		return `class="at-rowdot" role="img" aria-label="` + label + `" title="` + label + `"`
	}

	for state, flag := range map[string]string{
		"normal": "", "paused": "", "throttled": "Throttled", "unreachable": "Unreachable", "unknown": "Unknown", "disabled": "Disabled",
	} {
		html := render("downloaderrow", downloaderRowView{Name: "qBit", State: state, Cause: "nothing mapped is playing"})
		hasDot := strings.Contains(html, dot("Default rates"))
		if (flag == "") != hasDot || strings.Contains(html, ">Default rates<") {
			t.Errorf("downloader %s: dot %v in %s", state, hasDot, html)
		}
		if flag != "" && !strings.Contains(html, flag) {
			t.Errorf("downloader %s lost its %q flag", state, flag)
		}
		if hasDot && strings.Index(html, "at-rowdot") > strings.Index(html, "qBit") {
			t.Errorf("downloader %s: the dot comes after the name", state)
		}
	}
	for state, flag := range map[string]string{
		"idle": "", "playing": "2 playing", "unreachable": "Unreachable", "connecting": "Connecting…", "disabled": "Disabled",
	} {
		html := render("serverrow", serverRowView{Name: "Plex", State: state, Sessions: 2, Detail: "No activity detected."})
		hasDot := strings.Contains(html, dot("Idle"))
		if (flag == "") != hasDot || strings.Contains(html, ">Idle<") {
			t.Errorf("server %s: dot %v in %s", state, hasDot, html)
		}
		if flag != "" && !strings.Contains(html, flag) {
			t.Errorf("server %s lost its %q flag", state, flag)
		}
	}
}
