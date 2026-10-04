package auth

import (
	"crypto/rand"
	"encoding/base64"
	"time"
)

// SessionStore persists login sessions, so a restart or an update does
// not sign everyone out.
type SessionStore interface {
	CreateAuthSession(token string, expiresAt time.Time) error
	AuthSessionValid(token string, now time.Time) (bool, error)
	DeleteAuthSession(token string) error
}

// SessionManager issues and checks login sessions. A session lasts as
// long as its cookie: a fixed sessionTTL from login.
type SessionManager struct {
	store SessionStore
	ttl   time.Duration
}

func NewSessionManager(store SessionStore) *SessionManager {
	return &SessionManager{store: store, ttl: sessionTTL}
}

// Create returns a new opaque session token.
func (s *SessionManager) Create() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := s.store.CreateAuthSession(token, time.Now().Add(s.ttl)); err != nil {
		return "", err
	}
	return token, nil
}

// Valid reports whether token is a live, unexpired session.
func (s *SessionManager) Valid(token string) bool {
	if token == "" {
		return false
	}
	valid, err := s.store.AuthSessionValid(token, time.Now())
	return err == nil && valid
}

// Revoke ends a session immediately (logout).
func (s *SessionManager) Revoke(token string) {
	_ = s.store.DeleteAuthSession(token)
}
