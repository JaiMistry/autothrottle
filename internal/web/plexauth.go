package web

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

// plexSignIn tracks the one sign-in that can be in flight at a time.
//
// One is enough: this is a single-admin app and the flow lasts a couple
// of minutes. Keeping it in memory rather than in the database is
// deliberate — an abandoned attempt should evaporate on restart, not
// linger as a half-finished credential.
type plexSignIn struct {
	mu       sync.Mutex
	pin      clients.PlexPin
	started  time.Time
	clientID string
}

// plexPinTTL is how long an attempt is considered live. Plex expires
// its own PINs at around this point too.
const plexPinTTL = 10 * time.Minute

// startPlexSignIn asks plex.tv for a PIN and hands back the panel the
// user completes the flow in.
func (s *Server) startPlexSignIn(w http.ResponseWriter, r *http.Request) {
	clientID, err := randomClientID()
	if err != nil {
		s.plexSignInError(w, "Could not start sign-in.", err.Error())
		return
	}

	pin, err := clients.RequestPlexPin(r.Context(), clientID)
	if err != nil {
		s.plexSignInError(w, "Could not reach plex.tv.", err.Error())
		return
	}

	s.plex.mu.Lock()
	s.plex.pin = pin
	s.plex.clientID = clientID
	s.plex.started = time.Now()
	s.plex.mu.Unlock()

	s.renderPlexPanel(w, plexPanel{State: "waiting", AuthURL: pin.AuthURL})
}

// pollPlexSignIn is polled by the panel until Plex attaches a token.
func (s *Server) pollPlexSignIn(w http.ResponseWriter, r *http.Request) {
	s.plex.mu.Lock()
	pin, clientID, started := s.plex.pin, s.plex.clientID, s.plex.started
	s.plex.mu.Unlock()

	if pin.ID == 0 || time.Since(started) > plexPinTTL {
		s.plexSignInError(w, "This sign-in expired.", "Start it again to get a fresh code.")
		return
	}

	token, err := clients.PollPlexPin(r.Context(), clientID, pin.ID)
	if err != nil {
		s.plexSignInError(w, "Sign-in failed.", err.Error())
		return
	}
	if token == "" {
		// Still waiting on the user. Re-render the same panel so the
		// poll continues rather than reporting a failure.
		s.renderPlexPanel(w, plexPanel{State: "waiting", AuthURL: pin.AuthURL})
		return
	}

	s.plex.mu.Lock()
	s.plex.pin = clients.PlexPin{}
	s.plex.mu.Unlock()

	// The token is handed to the form rather than saved here: the user
	// still has to name the server and give its URL, and saving a
	// credential for a server that does not exist yet would leave an
	// orphan behind if they walk away.
	s.renderPlexPanel(w, plexPanel{State: "done", Token: token})
}

type plexPanel struct {
	State   string // idle | waiting | done | error
	AuthURL string
	Token   string
}

// plexSignInError reports the one failure the panel shows. The specific
// cause goes to the log rather than the page: every one of them ends in
// the same two options, so naming them only adds noise to the form.
func (s *Server) plexSignInError(w http.ResponseWriter, reason, detail string) {
	slog.Info("plex sign-in did not complete", "reason", reason, "detail", detail)
	s.renderPlexPanel(w, plexPanel{State: "error"})
}

// cancelPlexSignIn ends an attempt the user walked away from.
//
// Closing the Plex window leaves the PIN unclaimed, and the poll would
// otherwise sit on "waiting" until the ten-minute TTL ran out with no
// sign that anything was wrong.
func (s *Server) cancelPlexSignIn(w http.ResponseWriter, r *http.Request) {
	s.plex.mu.Lock()
	s.plex.pin = clients.PlexPin{}
	s.plex.mu.Unlock()
	s.plexSignInError(w, "window closed before approval", "")
}

func (s *Server) renderPlexPanel(w http.ResponseWriter, panel plexPanel) {
	s.renderFragment(w, "configuration", "plexsignin", panel)
}

// randomClientID identifies this install to plex.tv for the duration of
// one attempt. Fresh each time: it is not an identity, only a handle
// Plex uses to tie the PIN to the poller.
func randomClientID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating client id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
