package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What the 404 page shows as a log line is in the log.
func TestNotFoundIsLogged(t *testing.T) {
	app := newApp(t)
	expectStatus(t, app.get("/config/servers/9"), http.StatusNotFound)
	events, _ := app.db.ListEvents(10)
	if len(events) != 1 || events[0].Level != "warn" || events[0].Message != "no route for GET /config/servers/9" {
		t.Fatalf("events = %+v", events)
	}
	expectBody(t, app.get("/logs"), "no route for GET /config/servers/9")

	// The same address again soon after, and an address the browser asks
	// for by itself, add nothing.
	app.get("/config/servers/9")
	app.get("/favicon.ico")
	app.get("/apple-touch-icon-precomposed.png")
	if events, _ := app.db.ListEvents(10); len(events) != 1 {
		t.Errorf("%d events, want the first one only", len(events))
	}
	// Signed out counts too: that is where a dead link is followed most.
	anon := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	anon.get("/old-bookmark")
	if events, _ := anon.db.ListEvents(10); len(events) != 1 || events[0].Message != "no route for GET /old-bookmark" {
		t.Errorf("signed-out 404 events = %+v", events)
	}
}

func TestNotFoundLogLimits(t *testing.T) {
	var l notFoundLog
	t0 := time.Now()
	if !l.allow("GET /a", t0) || l.allow("GET /a", t0.Add(time.Minute)) {
		t.Error("an address repeated within the window was logged twice")
	}
	if !l.allow("GET /a", t0.Add(notFoundRepeat)) {
		t.Error("an address was never logged again")
	}
	// A scan: twenty a minute get through, the rest wait for the next.
	scan := t0.Add(time.Hour)
	allowed := 0
	for i := range 100 {
		if l.allow(fmt.Sprintf("GET /probe/%d", i), scan.Add(time.Duration(i)*time.Millisecond)) {
			allowed++
		}
	}
	if allowed != notFoundPerMinute {
		t.Errorf("%d of a 100-path scan logged in a minute, want %d", allowed, notFoundPerMinute)
	}
	if !l.allow("GET /later", scan.Add(time.Minute)) {
		t.Error("the next minute logged nothing")
	}
	if len(l.seen) > notFoundPerMinute+2 {
		t.Errorf("%d addresses remembered; old ones are not forgotten", len(l.seen))
	}
}

func TestNotFoundMessage(t *testing.T) {
	if got := notFoundMessage("GET", "/x"); got != "no route for GET /x" {
		t.Errorf("%q", got)
	}
	long := notFoundMessage("GET", "/"+strings.Repeat("é", 500))
	if !strings.HasSuffix(long, "…") || len([]rune(long)) != len([]rune("no route for GET "))+notFoundMaxPath+1 {
		t.Errorf("a long path is %d runes", len([]rune(long)))
	}
}
