package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// SessionManager tracks logged-in sessions in memory. Sessions don't
// need to survive a restart — for a single-admin homelab tool,
// requiring re-login after a container restart is an acceptable
// tradeoff for not needing a persisted session store at all.
// SessionStore persists login sessions. Optional: without one the
// manager keeps them in memory, which is fine for a test but signs
// everyone out on every restart.
type SessionStore interface {
	CreateAuthSession(token string, expiresAt time.Time) error
	AuthSessionValid(token string, now time.Time) (bool, error)
	DeleteAuthSession(token string) error
}

type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
	ttl      time.Duration
	// store, when set, is the durable copy. Sessions held only in
	// memory do not survive an update or a reboot, which on a
	// self-hosted app that auto-updates means being signed out
	// regularly for no reason the user can see.
	store SessionStore
}

// WithStore makes sessions durable.
func (s *SessionManager) WithStore(store SessionStore) *SessionManager {
	s.store = store
	return s
}

func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
	}
}

// Create returns a new opaque session token.
func (s *SessionManager) Create() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)

	expires := time.Now().Add(s.ttl)

	s.mu.Lock()
	s.sessions[token] = expires
	store := s.store
	s.mu.Unlock()

	if store != nil {
		if err := store.CreateAuthSession(token, expires); err != nil {
			return "", err
		}
	}
	return token, nil
}

// Valid reports whether token is a live, unexpired session, sliding
// its expiry forward on use.
func (s *SessionManager) Valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	expiry, ok := s.sessions[token]
	if ok {
		if time.Now().After(expiry) {
			delete(s.sessions, token)
			s.forgetStored(token)
			return false
		}
		s.sessions[token] = time.Now().Add(s.ttl)
		return true
	}

	// Not in memory. That is the normal state after a restart, not an
	// invalid token — so ask the durable copy before turning the user
	// away, and adopt it back into memory if it is still good.
	if s.store == nil {
		return false
	}
	valid, err := s.store.AuthSessionValid(token, time.Now())
	if err != nil || !valid {
		return false
	}
	s.sessions[token] = time.Now().Add(s.ttl)
	return true
}

// forgetStored drops a token from the durable copy, if there is one.
// Called with the lock held.
func (s *SessionManager) forgetStored(token string) {
	if s.store != nil {
		_ = s.store.DeleteAuthSession(token)
	}
}

// Revoke ends a session immediately (logout).
func (s *SessionManager) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
	s.forgetStored(token)
}
