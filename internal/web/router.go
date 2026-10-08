// Package web wires together the HTTP router, templates, and static
// assets.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/scheduler"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// pages lists the page templates that get paired with layout.html.
// Each is parsed as its own *template.Template — sharing one set across
// pages would collide, since every page defines a template named
// "content" and html/template's named templates share one namespace
// per *template.Template instance.
var pages = []string{"login", "setup", "dashboard", "configuration", "logs"}

// watcherManager is the watcher-lifecycle surface the web layer needs:
// after any media-server change, reconcile the running watchers so the
// change is live immediately rather than at next restart.
type watcherManager interface {
	Reconcile()
	Running(mediaServerID int64) bool
	Connection(mediaServerID int64) (clients.ConnectionState, bool)
}

type Server struct {
	db            *db.DB
	auth          *auth.Authenticator
	sessions      *auth.SessionManager
	limiter       *auth.LoginLimiter
	csrf          *auth.CSRF
	templates     map[string]*template.Template
	encryptionKey []byte
	scheduler     *scheduler.Scheduler
	watchers      watcherManager
	plex          plexSignIn
	assets        *staticAssets
	startedAt     time.Time
}

// parsePage builds one page's template set: the shared layout, the
// shared components, and the page itself. Each page is its own
// *template.Template because every one defines a template named
// "content", and those share a namespace per instance.
func parsePage(page string, funcs template.FuncMap) (*template.Template, error) {
	return template.New(page).Funcs(funcs).ParseFS(
		templateFS, "templates/layout.html", "templates/components.html", "templates/"+page+".html")
}

func New(database *db.DB, authenticator *auth.Authenticator, encryptionKey []byte, sched *scheduler.Scheduler, watchers watcherManager) (*Server, error) {
	templates := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := parsePage(page, templateFuncs())
		if err != nil {
			return nil, err
		}
		templates[page] = t
	}

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	csrf, err := auth.NewCSRF()
	if err != nil {
		return nil, err
	}

	return &Server{
		db:            database,
		auth:          authenticator,
		sessions:      auth.NewSessionManager(database),
		limiter:       auth.NewLoginLimiter(),
		csrf:          csrf,
		templates:     templates,
		encryptionKey: encryptionKey,
		scheduler:     sched,
		watchers:      watchers,
		assets:        newStaticAssets(staticSub),
		startedAt:     time.Now(),
	}, nil
}

// noStoreHTML makes every generated page revalidate.
//
// Without a directive a browser is free to apply its own heuristic and
// reuse the page — and iOS does. A stale page still references the
// previous build's content-hashed CSS, so a deploy appears not to have
// landed and a refresh does not fix it, because the refresh reuses the
// same cached HTML. Static assets are unaffected: they are hashed and
// set their own immutable headers.
func noStoreHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /login", noStoreHTML(http.HandlerFunc(s.loginPage)))
	mux.HandleFunc("POST /login", auth.LoginHandler(s.auth, s.sessions, s.limiter, s.loginFailed))
	mux.Handle("GET /setup", noStoreHTML(http.HandlerFunc(s.setupPage)))
	mux.HandleFunc("POST /setup", s.completeSetup)
	mux.HandleFunc("POST /logout", auth.LogoutHandler(s.sessions))
	mux.HandleFunc("GET /history", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/logs", http.StatusMovedPermanently)
	})

	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.dashboardPage)
	protected.HandleFunc("POST /pause-toggle", s.togglePause)
	protected.HandleFunc("GET /configuration", s.configurationPage)
	protected.HandleFunc("GET /logs", s.logsPage)
	protected.HandleFunc("GET /logs/rows", s.logRowsFragment)
	protected.HandleFunc("GET /logs/panel", s.logPanelFragment)
	protected.HandleFunc("POST /logs/clear", s.clearLogs)
	protected.HandleFunc("GET /dashboard/live", s.dashboardLive)
	protected.HandleFunc("GET /dashboard/history", s.historyCard)
	protected.HandleFunc("POST /configuration/media-servers", s.saveMediaServer)
	protected.HandleFunc("POST /configuration/media-servers/test", s.testMediaServerForm)
	protected.HandleFunc("POST /configuration/media-servers/{id}/delete", s.deleteMediaServer)
	protected.HandleFunc("POST /configuration/media-servers/{id}/test", s.testMediaServerByID)
	protected.HandleFunc("POST /configuration/downloaders", s.saveDownloader)
	protected.HandleFunc("POST /configuration/downloaders/test", s.testDownloaderForm)
	protected.HandleFunc("POST /configuration/downloaders/{id}/delete", s.deleteDownloader)
	protected.HandleFunc("POST /configuration/downloaders/{id}/test", s.testDownloaderByID)
	protected.HandleFunc("POST /configuration/mappings/set", s.setMapping)
	protected.HandleFunc("GET /configuration/update-check", s.updateCheck)
	protected.HandleFunc("POST /configuration/plex-signin", s.startPlexSignIn)
	protected.HandleFunc("GET /configuration/plex-signin/poll", s.pollPlexSignIn)
	protected.HandleFunc("POST /configuration/plex-signin/cancel", s.cancelPlexSignIn)

	// Wraps "/" only. "/static/" is a more specific pattern registered
	// below, so hashed assets keep their immutable headers.
	mux.Handle("/", noStoreHTML(auth.RequireAuth(s.auth, s.sessions)(
		auth.RequireCSRF(s.auth, s.csrf)(protected),
	)))

	if s.assets != nil {
		mux.Handle("/static/", http.StripPrefix("/static/", s.assets))
	}

	return mux
}
