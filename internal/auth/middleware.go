package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const (
	cookieName      = "autothrottle_session"
	csrfFormField   = "csrf_token"
	csrfHeaderName  = "X-CSRF-Token"
	sessionTTL      = 24 * time.Hour
	loginRateMax    = 5
	loginRateWindow = time.Minute
)

type contextKey string

const sessionTokenKey contextKey = "session_token"

// SessionTokenFromContext returns the current request's session token,
// as attached by RequireAuth. Empty when auth is disabled (no session
// exists) or the middleware hasn't run.
func SessionTokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(sessionTokenKey).(string)
	return token
}

// secureRequest reports whether this request reached us over HTTPS,
// directly or through a TLS-terminating reverse proxy that says so.
//
// The session cookie is marked Secure exactly when it is: over HTTPS
// that keeps the browser from ever sending it in the clear, and over
// plain http://host:8080 on a LAN a Secure cookie would simply never be
// sent back, so nobody could log in. Deciding per request means neither
// setup needs a setting. A client forging the forwarded header only
// makes its own cookie stricter.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// RequireAuth redirects to /login unless auth is disabled or the
// request carries a valid session cookie. On success, the session
// token is attached to the request context for RequireCSRF to use.
func RequireAuth(cfg Config, sessions *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Disabled {
				next.ServeHTTP(w, r)
				return
			}
			cookie, err := r.Cookie(cookieName)
			if err != nil || !sessions.Valid(cookie.Value) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			ctx := context.WithValue(r.Context(), sessionTokenKey, cookie.Value)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireCSRF validates the CSRF token on state-changing requests
// (anything other than GET/HEAD/OPTIONS). No-op when auth is disabled —
// without a session there's no synchronizer token to check against,
// and a disabled-auth deployment has already accepted that tradeoff.
func RequireCSRF(cfg Config, csrf *CSRF) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Disabled || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			token, _ := r.Context().Value(sessionTokenKey).(string)
			submitted := r.Header.Get(csrfHeaderName)
			if submitted == "" {
				submitted = r.FormValue(csrfFormField)
			}
			if !csrf.Valid(token, submitted) {
				http.Error(w, "invalid CSRF token", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LoginHandler handles POST /login: rate-limited credential check,
// session creation, redirect to the dashboard.
// LoginFailure renders a rejected attempt. It is a callback so this
// package keeps owning the credential logic without knowing anything
// about templates, and a failed login can still come back as the real
// login page rather than a plain-text error.
type LoginFailure func(w http.ResponseWriter, r *http.Request, status int, message string)

func LoginHandler(cfg Config, sessions *SessionManager, limiter *LoginLimiter, onFailure LoginFailure) http.HandlerFunc {
	fail := onFailure
	if fail == nil {
		fail = func(w http.ResponseWriter, _ *http.Request, status int, message string) {
			http.Error(w, message, status)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			fail(w, r, http.StatusTooManyRequests, "Too many attempts. Wait a moment and try again.")
			return
		}
		username := r.FormValue("username")
		password := r.FormValue("password")
		if username == "" || password == "" {
			fail(w, r, http.StatusUnauthorized, "Enter a username and password.")
			return
		}
		if !cfg.Verify(username, password) {
			// Deliberately generic: saying which half was wrong would
			// confirm whether a username exists.
			fail(w, r, http.StatusUnauthorized, "Invalid username or password.")
			return
		}
		token, err := sessions.Create()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		setSessionCookie(w, r, token)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// LogoutHandler revokes the session and clears the cookie.
func LogoutHandler(sessions *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(cookieName); err == nil {
			sessions.Revoke(cookie.Value)
		}
		clearSessionCookie(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// NewLoginLimiter with the app's standard rate: 5 attempts per minute.
func NewDefaultLoginLimiter() *LoginLimiter {
	return NewLoginLimiter(loginRateMax, loginRateWindow)
}

// NewDefaultSessionManager with the app's standard 24-hour session TTL.
func NewDefaultSessionManager() *SessionManager {
	return NewSessionManager(sessionTTL)
}
