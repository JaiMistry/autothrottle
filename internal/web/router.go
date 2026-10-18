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
	"github.com/JaiMistry/autothrottle/internal/live"
	"github.com/JaiMistry/autothrottle/internal/scheduler"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// pages lists the page templates paired with layout.html.
var pages = []string{"login", "setup", "dashboard", "configuration", "logs", "notfound"}

// watcherManager is the watcher surface the web layer needs.
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
	live          *live.Hub
	notFoundLog   notFoundLog
	localStatus   bool
}

// parsePage builds one page's template set: layout, components and the
// page. Each page gets its own set because every one defines "content".
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
		live:          live.NewHub(),
	}, nil
}

// noStoreHTML makes every generated page revalidate. Otherwise browsers
// (iOS especially) reuse a page that points at the previous build's
// hashed CSS, and a refresh does not fix it.
func noStoreHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

// securityHeaders goes on every response. The referrer policy keeps this
// app's (often private) address out of requests its outside links make.
// Framing is allowed on purpose: the SameSite=Lax cookie keeps another
// site's frame signed out, and dashboards that embed the app keep working.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
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
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /history", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/logs", http.StatusMovedPermanently)
	})
	if s.localStatus {
		mux.HandleFunc("GET /local/status", s.localStatusHandler)
	}

	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.dashboardPage)
	protected.HandleFunc("POST /pause-toggle", s.togglePause)
	protected.HandleFunc("GET /configuration", s.configurationPage)
	protected.HandleFunc("GET /logs", s.logsPage)
	protected.HandleFunc("GET /logs/rows", s.logRowsFragment)
	protected.HandleFunc("GET /logs/panel", s.logPanelFragment)
	protected.HandleFunc("POST /logs/clear", s.clearLogs)
	protected.HandleFunc("GET /dashboard/live", s.dashboardLive)
	protected.HandleFunc("GET /paused-banner", s.pausedBanner)
	protected.HandleFunc("GET /events", s.liveEvents)
	protected.HandleFunc("GET /dashboard/history", s.historyCard)
	protected.HandleFunc("GET /configuration/connections/status", s.connectionStatus)
	protected.HandleFunc("GET /configuration/media-servers/new", s.sheetForm("server"))
	protected.HandleFunc("GET /configuration/media-servers/{id}/edit", s.sheetForm("server"))
	protected.HandleFunc("GET /configuration/downloaders/new", s.sheetForm("downloader"))
	protected.HandleFunc("GET /configuration/downloaders/{id}/edit", s.sheetForm("downloader"))
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

	// "/static/" below is more specific, so assets skip noStoreHTML.
	app := auth.RequireAuth(s.auth, s.sessions)(auth.RequireCSRF(s.auth, s.csrf)(protected))
	mux.Handle("/", noStoreHTML(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Until an account exists, every address is the create-account screen.
		if s.auth.NeedsSetup() {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		// Real pages ask for a sign-in; an address with no page gets the
		// 404 signed in or not, as there is nothing behind it to protect.
		if _, pattern := protected.Handler(r); pattern == "" {
			s.notFound(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})))

	if s.assets != nil {
		mux.Handle("/static/", http.StripPrefix("/static/", s.assets))
	}

	return securityHeaders(mux)
}
