package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/scheduler"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// fakeWatchers stands in for the watcher manager, the one part of the
// app that would otherwise open real sockets to media servers.
type fakeWatchers struct {
	mu         sync.Mutex
	reconciles int
	running    map[int64]bool
}

func (f *fakeWatchers) Reconcile() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciles++
}

func (f *fakeWatchers) Running(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[id]
}

func (f *fakeWatchers) Connection(int64) (clients.ConnectionState, bool) {
	return clients.ConnectionState{Connected: true}, true
}

// testApp is the real router over a real database and scheduler. Only
// the media-server watchers are faked.
type testApp struct {
	t        *testing.T
	srv      *Server
	db       *db.DB
	sched    *scheduler.Scheduler
	watchers *fakeWatchers
	handler  http.Handler
	cookies  []*http.Cookie
	csrf     string
	// qbit is a stand-in qBittorrent that accepts everything, so the
	// scheduler's real rate-setting calls succeed rather than failing in
	// the background and writing error events a test then trips over.
	qbit *httptest.Server
}

const (
	testUser = "admin"
	testPass = "correct-horse-battery"
)

// newApp starts an instance with environment credentials, already
// signed in. newAppWith lets a test choose the auth setup itself.
func newApp(t *testing.T) *testApp {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	app.login()
	return app
}

func newAppWith(t *testing.T, env map[string]string) *testApp {
	t.Helper()
	for _, k := range []string{"AUTH_USERNAME", "AUTH_PASSWORD", "AUTH_DISABLED"} {
		t.Setenv(k, env[k])
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	authenticator, err := auth.Load(database)
	if err != nil {
		t.Fatalf("loading auth: %v", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	sched := scheduler.New(database, key, settings.New(database))
	watchers := &fakeWatchers{running: map[int64]bool{}}
	srv, err := New(database, authenticator, key, sched, watchers)
	if err != nil {
		t.Fatalf("building server: %v", err)
	}

	qbit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test"})
		}
		_, _ = w.Write([]byte("Ok."))
	}))
	t.Cleanup(qbit.Close)

	return &testApp{t: t, srv: srv, db: database, sched: sched, watchers: watchers, handler: srv.Router(), qbit: qbit}
}

var csrfField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// do sends a request carrying the session cookie, and keeps any cookie
// the response sets — enough of a browser for these tests.
func (a *testApp) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	a.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	for _, c := range a.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		a.cookies = append(slices.DeleteFunc(a.cookies, func(old *http.Cookie) bool { return old.Name == c.Name }), c)
	}
	return rec
}

func (a *testApp) get(path string) *httptest.ResponseRecorder { return a.do("GET", path, nil) }

// post submits a form the way the app's own pages do, CSRF token included.
func (a *testApp) post(path string, form url.Values) *httptest.ResponseRecorder {
	a.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if a.csrf != "" && form.Get("csrf_token") == "" {
		form.Set("csrf_token", a.csrf)
	}
	return a.do("POST", path, form)
}

func (a *testApp) login() {
	a.t.Helper()
	rec := a.do("POST", "/login", url.Values{"username": {testUser}, "password": {testPass}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		a.t.Fatalf("login: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	m := csrfField.FindStringSubmatch(a.get("/configuration").Body.String())
	if m == nil {
		a.t.Fatal("no CSRF token on the configuration page")
	}
	a.csrf = m[1]
}

// mediaServer and downloader create records through the database, for
// tests whose subject is something other than the create form itself.
func (a *testApp) mediaServer(name, msType, rawURL string) int64 {
	a.t.Helper()
	id, err := a.db.CreateMediaServer(db.MediaServer{Name: name, Type: msType, URL: rawURL, Enabled: true})
	if err != nil {
		a.t.Fatalf("creating media server: %v", err)
	}
	return id
}

func (a *testApp) downloader(name string) int64 {
	a.t.Helper()
	id, err := a.db.CreateDownloader(db.Downloader{
		Name: name, Type: "qbittorrent", URL: a.qbit.URL, Enabled: true,
		BackupDownloadLimit: 1500, BackupUploadLimit: 300,
	})
	if err != nil {
		a.t.Fatalf("creating downloader: %v", err)
	}
	return id
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d; body: %.300s", rec.Code, want, rec.Body.String())
	}
}

func expectRedirect(t *testing.T, rec *httptest.ResponseRecorder, to string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound && rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status %d, want a redirect to %s; body: %.300s", rec.Code, to, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != to {
		t.Fatalf("redirected to %q, want %q", got, to)
	}
}

func expectBody(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("response does not contain %q; body: %.600s", want, rec.Body.String())
	}
}
