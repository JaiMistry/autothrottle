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

// plexSignIn tracks the one sign-in in flight. In memory on purpose: an
// abandoned attempt should vanish on restart, not linger.
type plexSignIn struct {
	mu       sync.Mutex
	pin      clients.PlexPin
	started  time.Time
	clientID string
}

// plexPinTTL is how long an attempt is live; Plex expires its PINs about
// then too.
const plexPinTTL = 10 * time.Minute

// startPlexSignIn asks plex.tv for a PIN and renders the waiting panel.
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
		// Still waiting: re-render so the poll continues.
		s.renderPlexPanel(w, plexPanel{State: "waiting", AuthURL: pin.AuthURL})
		return
	}

	s.plex.mu.Lock()
	s.plex.pin = clients.PlexPin{}
	s.plex.mu.Unlock()

	// Handed to the form, not saved: the server does not exist until the
	// user saves it.
	s.renderPlexPanel(w, plexPanel{State: "done", Token: token})
}

type plexPanel struct {
	State   string // idle | waiting | done | error
	AuthURL string
	Token   string
}

// plexSignInError shows the panel's one failure state; the cause goes to
// the log, since every cause leaves the same two options.
func (s *Server) plexSignInError(w http.ResponseWriter, reason, detail string) {
	slog.Info("plex sign-in did not complete", "reason", reason, "detail", detail)
	s.renderPlexPanel(w, plexPanel{State: "error"})
}

// cancelPlexSignIn ends an attempt whose Plex window was closed, rather
// than leaving the panel waiting out the TTL.
func (s *Server) cancelPlexSignIn(w http.ResponseWriter, r *http.Request) {
	s.plex.mu.Lock()
	s.plex.pin = clients.PlexPin{}
	s.plex.mu.Unlock()
	s.plexSignInError(w, "window closed before approval", "")
}

func (s *Server) renderPlexPanel(w http.ResponseWriter, panel plexPanel) {
	s.renderFragment(w, "configuration", "plexsignin", panel)
}

// randomClientID is a fresh handle per attempt that ties the PIN to the
// poller; it is not an identity.
func randomClientID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating client id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
