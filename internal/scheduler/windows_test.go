package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/schedule"
)

// fakeController is a fully managed client: it records what it's put
// into and reports whatever state the test sets.
type fakeController struct {
	fakeLimiter
	mu      sync.Mutex
	applied []clients.Desired
	state   clients.ClientState
	cleared int
}

func (f *fakeController) Inspect(context.Context, string, string, string) (clients.ClientState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (f *fakeController) Apply(_ context.Context, _, _, _ string, want clients.Desired) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, want)
	// The client now holds what it was given.
	f.state.Normal = want.Normal
	if want.Alternative != nil {
		f.state.Alternative = *want.Alternative
	}
	if want.AlternativeOn != nil {
		f.state.AlternativeOn = *want.AlternativeOn
	}
	return nil
}

func (f *fakeController) ClearConflicts(context.Context, string, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared++
	f.state.Scheduler, f.state.AlternativeOn = "", false
	return nil
}

func (f *fakeController) last(t *testing.T) clients.Desired {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) == 0 {
		t.Fatal("nothing was applied")
	}
	return f.applied[len(f.applied)-1]
}

func (f *fakeController) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func (f *fakeController) set(fn func(*clients.ClientState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.state)
}

func withController(s *Scheduler) *fakeController {
	c := &fakeController{}
	s.controllerFor = func(string) clients.Controller { return c }
	return c
}

// allDay is in force whenever a test runs.
func allDay(down, up int64, throttle bool) schedule.Window {
	return schedule.Window{Days: schedule.EveryDay, Start: 0, End: 0, DownloadKBps: down, UploadKBps: up, Throttle: throttle}
}

func addWindow(t *testing.T, database *db.DB, dlID int64, w schedule.Window) int64 {
	t.Helper()
	id, err := database.CreateWindow(dlID, w)
	if err != nil {
		t.Fatalf("creating window: %v", err)
	}
	return id
}

func mapped(t *testing.T, database *db.DB, msID, dlID int64) {
	t.Helper()
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}
}

func wantEvent(t *testing.T, database *db.DB, contains string) {
	t.Helper()
	events, err := database.ListEvents(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.Contains(e.Message, contains) {
			return
		}
	}
	t.Errorf("no event containing %q in %d events", contains, len(events))
}

func countEvents(t *testing.T, database *db.DB, contains string) int {
	t.Helper()
	events, err := database.ListEvents(200)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if strings.Contains(e.Message, contains) {
			n++
		}
	}
	return n
}

// Without windows or alternative speed, a downloader is set exactly as
// before: its limits only, through the plain limiter.
func TestUnmanagedDownloaderKeepsTheOldWrites(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	controller := withController(s)
	_, _ = addServerAndDownloader(t, database)
	s.resolve(context.Background())
	if got := limiter.snapshot(); len(got) != 1 {
		t.Fatalf("limiter calls = %+v", got)
	}
	if controller.count() != 0 {
		t.Fatal("an unmanaged downloader was fully managed")
	}
}

// A window's rates replace Default, and alternative speed is kept off so
// they apply.
func TestWindowReplacesDefault(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	controller := withController(s)
	_, dlID := addServerAndDownloader(t, database)
	addWindow(t, database, dlID, allDay(3000, 700, true))

	s.resolve(context.Background())
	got := controller.last(t)
	if got.Normal != (clients.Limits{DownloadKBps: 3000, UploadKBps: 700}) || got.Alternative != nil || got.AlternativeOn == nil || *got.AlternativeOn {
		t.Fatalf("applied %+v", got)
	}
	if len(limiter.snapshot()) != 0 {
		t.Fatal("a managed downloader went through the plain limiter")
	}
	state := s.State(dlID)
	if state.Window != "00:00–00:00" || state.DownloadKBps != 3000 || state.Until.IsZero() || state.Then == "" {
		t.Fatalf("state = %+v", state)
	}
	wantEvent(t, database, "applied qbit's 00:00–00:00 window rate")
}

// Throttling never raises a limit: the window's slower download stays,
// and the throttle still caps the window's unlimited upload.
func TestThrottleInsideAWindowTakesTheLowerLimit(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	controller := withController(s)
	msID, dlID := addServerAndDownloader(t, database) // Throttled 500 / 100
	mapped(t, database, msID, dlID)
	addWindow(t, database, dlID, allDay(300, 0, true))

	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, true)
	s.resolve(context.Background())
	if got := controller.last(t).Normal; got != (clients.Limits{DownloadKBps: 300, UploadKBps: 100}) {
		t.Fatalf("throttled to %+v", got)
	}
	if !s.State(dlID).Throttled {
		t.Fatal("not throttled")
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, false)
	s.resolve(context.Background())
	if got := controller.last(t).Normal; got != (clients.Limits{DownloadKBps: 300}) {
		t.Fatalf("restored to %+v", got)
	}
	wantEvent(t, database, "restored qbit's 00:00–00:00 window rate (nothing mapped is playing)")
}

// A window that says don't throttle lets playback through, and says so.
func TestWindowThatDoesNotThrottle(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	controller := withController(s)
	msID, dlID := addServerAndDownloader(t, database)
	mapped(t, database, msID, dlID)
	addWindow(t, database, dlID, schedule.Window{Name: "Night", Days: schedule.EveryDay, Throttle: false})

	s.resolve(context.Background())
	before := controller.count()
	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, true)
	s.resolve(context.Background())

	state := s.State(dlID)
	if state.Throttled || !state.Suppressed || state.Window != "Night" {
		t.Fatalf("state = %+v", state)
	}
	if controller.count() != before {
		t.Fatal("the client was written for a throttle that didn't happen")
	}
	if since, ok := s.ThrottlingSince(); ok {
		t.Fatalf("throttle history opened at %v", since)
	}
	wantEvent(t, database, "not throttling qbit while Plex plays: its Night window allows it")
}

// With alternative speed, Default stays in the ordinary limits and the
// throttle lives in the alternative ones, switched on while playing.
func TestAlternativeSpeedCarriesTheThrottle(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	controller := withController(s)
	msID, dlID := addServerAndDownloader(t, database)
	mapped(t, database, msID, dlID)
	dl, err := database.GetDownloader(dlID)
	if err != nil {
		t.Fatal(err)
	}
	dl.DefaultDownloadLimit, dl.AlternativeSpeed = 9000, true
	if err := database.UpdateDownloader(dl); err != nil {
		t.Fatal(err)
	}

	s.resolve(context.Background())
	got := controller.last(t)
	if got.Normal.DownloadKBps != 9000 || *got.Alternative != (clients.Limits{DownloadKBps: 500, UploadKBps: 100}) || *got.AlternativeOn {
		t.Fatalf("idle: %+v", got)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, true)
	s.resolve(context.Background())
	got = controller.last(t)
	if got.Normal.DownloadKBps != 9000 || !*got.AlternativeOn {
		t.Fatalf("playing: %+v", got)
	}
	if state := s.State(dlID); !state.Throttled || state.DownloadKBps != 500 {
		t.Fatalf("state = %+v", state)
	}
}

// Deluge has no alternative speed: managed, it's only given its limits.
func TestManagedWithoutAlternativeSpeed(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	controller := withController(s)
	dlID, err := database.CreateDownloader(db.Downloader{Name: "deluge", Type: "deluge", URL: "http://deluge.invalid", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	addWindow(t, database, dlID, allDay(100, 50, true))
	s.resolve(context.Background())
	if got := controller.last(t); got.AlternativeOn != nil || got.Alternative != nil || got.Normal.DownloadKBps != 100 {
		t.Fatalf("applied %+v", got)
	}
}

// Leaving, AutoThrottle restores Default even inside a window: nothing
// would be left to end it.
func TestExitRestoresDefaultInsideAWindow(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	controller := withController(s)
	_, dlID := addServerAndDownloader(t, database)
	addWindow(t, database, dlID, allDay(100, 50, true))
	s.resolve(context.Background())
	s.Release(context.Background())
	if got := controller.last(t).Normal; got != (clients.Limits{}) {
		t.Fatalf("on exit: %+v", got)
	}
}

// The clock notices a window that has started or gone.
func TestWindowsMoved(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	withController(s)
	_, dlID := addServerAndDownloader(t, database)
	s.resolve(context.Background())
	now := time.Now()
	if s.windowsMoved(now) {
		t.Fatal("moved with no windows")
	}
	windowID := addWindow(t, database, dlID, allDay(100, 0, true))
	if !s.windowsMoved(now) {
		t.Fatal("a window in force went unnoticed")
	}
	s.resolve(context.Background())
	if s.windowsMoved(now) {
		t.Fatal("moved once applied")
	}
	if err := database.DeleteWindow(dlID, windowID); err != nil {
		t.Fatal(err)
	}
	if !s.windowsMoved(now) {
		t.Fatal("a removed window went unnoticed")
	}
}

func TestNextEdge(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	_, dlID := addServerAndDownloader(t, database)
	from := time.Date(2026, 10, 21, 8, 0, 0, 0, time.Local)
	if _, ok := s.nextEdge(from); ok {
		t.Fatal("an edge with no windows")
	}
	addWindow(t, database, dlID, schedule.Window{Days: schedule.EveryDay, Start: 9 * 60, End: 17 * 60, Throttle: true})
	next, ok := s.nextEdge(from)
	if want := time.Date(2026, 10, 21, 9, 0, 0, 0, time.Local); !ok || !next.Equal(want) {
		t.Fatalf("next edge %v %v, want %v", next, ok, want)
	}
}

// A managed downloader changed from outside is put back, and the change
// logged; a reading taken before the last apply isn't mistaken for one.
func TestChangesOutsideAreSetBack(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	controller := withController(s)
	_, dlID := addServerAndDownloader(t, database)
	addWindow(t, database, dlID, allDay(100, 50, true))
	s.resolve(context.Background())
	dl, _ := database.GetDownloader(dlID)

	s.inspectOne(context.Background(), dl, true)
	if controller.count() != 1 {
		t.Fatal("put back with nothing changed")
	}

	controller.set(func(st *clients.ClientState) { st.Normal.DownloadKBps = 99999; st.AlternativeOn = true })
	s.inspectOne(context.Background(), dl, true)
	if controller.count() != 2 || controller.last(t).Normal.DownloadKBps != 100 || *controller.last(t).AlternativeOn {
		t.Fatalf("not put back: %+v", controller.last(t))
	}
	wantEvent(t, database, "qbit's limits were changed outside AutoThrottle, so they were set back")

	// Unmanaged downloaders are only read, never corrected.
	controller.set(func(st *clients.ClientState) { st.Normal.DownloadKBps = 1 })
	s.inspectOne(context.Background(), dl, false)
	if controller.count() != 2 {
		t.Fatal("an unmanaged downloader was corrected")
	}
}

// The client's own scheduler is reported once, shown on the Dashboard,
// and cleared on request.
func TestConflictReportedAndCleared(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	controller := withController(s)
	_, dlID := addServerAndDownloader(t, database)
	s.resolve(context.Background())
	dl, _ := database.GetDownloader(dlID)

	if err := s.ClearConflicts(context.Background(), dlID); !errors.Is(err, ErrNoConflict) {
		t.Fatalf("clearing nothing: %v", err)
	}

	controller.set(func(st *clients.ClientState) { st.Scheduler = "alternative speed schedule" })
	s.inspectOne(context.Background(), dl, false)
	s.inspectOne(context.Background(), dl, false)
	if c := s.State(dlID).Conflict; !strings.Contains(c, "qbit's own alternative speed schedule is on") {
		t.Fatalf("conflict = %q", c)
	}
	if n := countEvents(t, database, "alternative speed schedule is on"); n != 1 {
		t.Fatalf("warned %d times", n)
	}

	if err := s.ClearConflicts(context.Background(), dlID); err != nil {
		t.Fatal(err)
	}
	if controller.cleared != 1 || s.State(dlID).Conflict != "" {
		t.Fatalf("cleared %d, conflict %q", controller.cleared, s.State(dlID).Conflict)
	}
	wantEvent(t, database, "turned off qbit's alternative speed schedule from AutoThrottle")
}

// Alternative speed left on is a conflict only where AutoThrottle doesn't
// switch it itself.
func TestAlternativeSpeedOnIsAConflictWhenUnmanaged(t *testing.T) {
	dl := db.Downloader{Name: "qbit"}
	on := clients.ClientState{AlternativeOn: true}
	if conflictFor(dl, on, false) == "" {
		t.Error("unmanaged: no conflict")
	}
	if conflictFor(dl, on, true) != "" {
		t.Error("managed: a conflict")
	}
}

func TestLowerLimit(t *testing.T) {
	for _, tc := range []struct{ a, b, want int64 }{
		{0, 0, 0}, {500, 0, 500}, {0, 300, 300}, {500, 300, 300}, {200, 300, 200},
	} {
		if got := lowerLimit(tc.a, tc.b); got != tc.want {
			t.Errorf("lowerLimit(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
