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
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
	ttl      time.Duration
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

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[token] = time.Now().Add(s.ttl)
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
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(s.sessions, token)
		return false
	}
	s.sessions[token] = time.Now().Add(s.ttl)
	return true
}

// Revoke ends a session immediately (logout).
func (s *SessionManager) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}
