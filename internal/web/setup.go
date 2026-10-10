package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/version"
)

type setupData struct {
	pageData
	Username string
	// Token is the form's half of the setup CSRF check; its cookie
	// carries the other.
	Token string
}

// setupCookie pairs the setup form with the browser that loaded it.
// There is no session yet to hang a CSRF token on, so the token is a
// cookie and a matching form field: another site can make a browser
// post the form, but it cannot read or set this cookie, and with
// SameSite=Strict the browser will not even send it along.
const setupCookie = "autothrottle_setup"

func (s *Server) renderSetup(w http.ResponseWriter, r *http.Request, status int, data setupData) {
	data.Title = "Set up"
	data.Version = version.Label()
	data.Asset = s.assets.URL
	if c, err := r.Cookie(setupCookie); err == nil && c.Value != "" {
		data.Token = c.Value
	} else {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		data.Token = base64.RawURLEncoding.EncodeToString(buf)
		http.SetCookie(w, &http.Cookie{
			Name: setupCookie, Value: data.Token, Path: "/setup",
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
			Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		})
	}
	w.WriteHeader(status)
	s.render(w, "setup", data)
}

// setupPage exists only while there is no way to log in. After that the
// address is gone — a 404 like any other — so it can never be used to
// replace an account that is already there.
func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if !s.auth.NeedsSetup() {
		s.notFound(w, r)
		return
	}
	s.renderSetup(w, r, http.StatusOK, setupData{})
}

func (s *Server) completeSetup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.NeedsSetup() {
		s.notFound(w, r)
		return
	}
	username := r.FormValue("username")
	retry := setupData{Username: username}

	c, err := r.Cookie(setupCookie)
	if err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.FormValue("csrf_token"))) != 1 {
		retry.Error = "That form had expired. Enter your details again."
		s.renderSetup(w, r, http.StatusForbidden, retry)
		return
	}
	// Shares the login limiter, so the one page open to anyone cannot
	// be hammered either.
	if !s.limiter.Allow() {
		retry.Error = "Too many attempts. Wait a moment and try again."
		s.renderSetup(w, r, http.StatusTooManyRequests, retry)
		return
	}

	err = s.auth.CompleteSetup(username, r.FormValue("password"))
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrUsernameEmpty), errors.Is(err, auth.ErrPasswordEmpty):
		retry.Error = "Enter a username and password."
		s.renderSetup(w, r, http.StatusUnprocessableEntity, retry)
		return
	case errors.Is(err, auth.ErrSetupDone):
		s.notFound(w, r)
		return
	default:
		slog.Error("completing setup", "error", err)
		retry.Error = "The account could not be saved. Check the container log."
		s.renderSetup(w, r, http.StatusInternalServerError, retry)
		return
	}

	slog.Info("setup complete: account created", "username", s.auth.Username())
	http.SetCookie(w, &http.Cookie{Name: setupCookie, Path: "/setup", MaxAge: -1})
	if err := auth.StartSession(w, r, s.sessions); err != nil {
		// The account exists; only the automatic sign-in failed.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
