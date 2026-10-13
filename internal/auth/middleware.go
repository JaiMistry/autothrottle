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

// SessionTokenFromContext returns the session token RequireAuth
// attached, or "" when auth is disabled.
func SessionTokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(sessionTokenKey).(string)
	return token
}

// secureRequest reports whether the request came over HTTPS, directly or
// via a proxy that says so. The cookie is Secure exactly then: a Secure
// cookie over plain LAN http would never be sent back.
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

// RequireAuth sends a request without a valid session to /login, or to
// /setup while there is no account, and attaches the session token for
// RequireCSRF.
func RequireAuth(a *Authenticator, sessions *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a.Disabled() {
				next.ServeHTTP(w, r)
				return
			}
			if a.NeedsSetup() {
				redirect(w, r, "/setup")
				return
			}
			cookie, err := r.Cookie(cookieName)
			if err != nil || !sessions.Valid(cookie.Value) {
				redirect(w, r, loginURL(r))
				return
			}
			ctx := context.WithValue(r.Context(), sessionTokenKey, cookie.Value)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireCSRF checks the CSRF token on anything but GET, HEAD and
// OPTIONS. With auth disabled there is no session to check against.
func RequireCSRF(a *Authenticator, csrf *CSRF) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a.Disabled() || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
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

// LoginFailure renders a rejected attempt as the real login page, while
// this package keeps the credential logic.
type LoginFailure func(w http.ResponseWriter, r *http.Request, status int, message string)

// LoginHandler handles POST /login: rate-limited check, a new session,
// then back to the page that asked, or the Dashboard.
func LoginHandler(a *Authenticator, sessions *SessionManager, limiter *LoginLimiter, fail LoginFailure) http.HandlerFunc {
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
		if !a.Verify(username, password) {
			// Generic: naming the wrong half would confirm a username.
			fail(w, r, http.StatusUnauthorized, "Invalid username or password.")
			return
		}
		if err := StartSession(w, r, sessions); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, SafeNext(r.FormValue("next")), http.StatusSeeOther)
	}
}

// StartSession signs the browser in after setup, without asking for the
// password again.
func StartSession(w http.ResponseWriter, r *http.Request, sessions *SessionManager) error {
	token, err := sessions.Create()
	if err != nil {
		return err
	}
	setSessionCookie(w, r, token)
	return nil
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
