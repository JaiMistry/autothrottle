package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/version"
)

type setupData struct {
	pageData
	Username string
}

func (s *Server) renderSetup(w http.ResponseWriter, status int, data setupData) {
	data.Title = "Set up"
	data.Version = version.Label()
	data.Asset = s.assets.URL
	w.WriteHeader(status)
	if err := s.templates["setup"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering setup page", "error", err)
	}
}

// setupPage is only ever reachable while there is no way to log in.
// Once an account exists, or credentials come from the environment, it
// sends you to the login page instead — so it can never be used to
// replace an account that is already there.
func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if !s.auth.NeedsSetup() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.renderSetup(w, http.StatusOK, setupData{})
}

func (s *Server) completeSetup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.NeedsSetup() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	username := r.FormValue("username")
	retry := setupData{Username: username}

	// Shares the login limiter: guessing setup codes is guessing a
	// password, and gets the same few tries a minute.
	if !s.limiter.Allow() {
		retry.Error = "Too many attempts. Wait a moment and try again."
		s.renderSetup(w, http.StatusTooManyRequests, retry)
		return
	}
	if r.FormValue("password") != r.FormValue("confirm") {
		retry.Error = "The two passwords don't match."
		s.renderSetup(w, http.StatusUnprocessableEntity, retry)
		return
	}

	err := s.auth.CompleteSetup(r.FormValue("code"), username, r.FormValue("password"))
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrBadSetupCode), errors.Is(err, auth.ErrUsernameEmpty), errors.Is(err, auth.ErrPasswordShort):
		retry.Error = capitalize(err.Error()) + "."
		s.renderSetup(w, http.StatusUnprocessableEntity, retry)
		return
	case errors.Is(err, auth.ErrSetupDone):
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	default:
		slog.Error("completing setup", "error", err)
		retry.Error = "The account could not be saved. Check the container log."
		s.renderSetup(w, http.StatusInternalServerError, retry)
		return
	}

	slog.Info("setup complete: account created", "username", s.auth.Username())
	if err := auth.StartSession(w, r, s.sessions); err != nil {
		// The account exists; only the automatic sign-in failed.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
