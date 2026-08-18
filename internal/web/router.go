// Package web wires together the HTTP router, templates, and static
// assets.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/JaiMistry/autothrottle/internal/auth"
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
var pages = []string{"login", "dashboard", "configuration"}

type Server struct {
	db            *db.DB
	authCfg       auth.Config
	sessions      *auth.SessionManager
	limiter       *auth.LoginLimiter
	csrf          *auth.CSRF
	templates     map[string]*template.Template
	encryptionKey []byte
	scheduler     *scheduler.Scheduler
}

func New(database *db.DB, authCfg auth.Config, encryptionKey []byte, sched *scheduler.Scheduler) (*Server, error) {
	templates := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		templates[page] = t
	}

	csrf, err := auth.NewCSRF()
	if err != nil {
		return nil, err
	}

	return &Server{
		db:            database,
		authCfg:       authCfg,
		sessions:      auth.NewDefaultSessionManager(),
		limiter:       auth.NewDefaultLoginLimiter(),
		csrf:          csrf,
		templates:     templates,
		encryptionKey: encryptionKey,
		scheduler:     sched,
	}, nil
}

func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", auth.LoginHandler(s.authCfg, s.sessions, s.limiter))
	mux.HandleFunc("POST /logout", auth.LogoutHandler(s.sessions))
	mux.HandleFunc("GET /health", s.healthHandler)

	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.dashboardPage)
	protected.HandleFunc("POST /pause-toggle", s.togglePause)
	protected.HandleFunc("GET /configuration", s.configurationPage)
	protected.HandleFunc("POST /configuration/media-servers", s.saveMediaServer)
	protected.HandleFunc("POST /configuration/media-servers/test", s.testMediaServerForm)
	protected.HandleFunc("POST /configuration/media-servers/{id}/delete", s.deleteMediaServer)
	protected.HandleFunc("POST /configuration/media-servers/{id}/test", s.testMediaServerByID)
	protected.HandleFunc("POST /configuration/downloaders", s.saveDownloader)
	protected.HandleFunc("POST /configuration/downloaders/test", s.testDownloaderForm)
	protected.HandleFunc("POST /configuration/downloaders/{id}/delete", s.deleteDownloader)
	protected.HandleFunc("POST /configuration/downloaders/{id}/test", s.testDownloaderByID)
	protected.HandleFunc("POST /configuration/mappings", s.createMapping)
	protected.HandleFunc("POST /configuration/mappings/{id}/toggle", s.toggleMapping)
	protected.HandleFunc("POST /configuration/mappings/{id}/delete", s.deleteMapping)
	// /analytics, /history, /settings land in later passes.

	mux.Handle("/", auth.RequireAuth(s.authCfg, s.sessions)(
		auth.RequireCSRF(s.authCfg, s.csrf)(protected),
	))

	staticSub, err := fs.Sub(staticFS, "static")
	if err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	}

	return mux
}
