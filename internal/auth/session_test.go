package auth

import (
	"testing"
	"time"
)

// memStore is a durable store standing in for the database.
type memStore struct {
	rows map[string]time.Time
}

func (m *memStore) CreateAuthSession(token string, expiresAt time.Time) error {
	if m.rows == nil {
		m.rows = map[string]time.Time{}
	}
	m.rows[token] = expiresAt
	return nil
}

func (m *memStore) AuthSessionValid(token string, now time.Time) (bool, error) {
	expiry, ok := m.rows[token]
	return ok && now.Before(expiry), nil
}

func (m *memStore) DeleteAuthSession(token string) error { delete(m.rows, token); return nil }

func TestSessionManager(t *testing.T) {
	store := &memStore{}
	sm := NewSessionManager(store)
	token, err := sm.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !sm.Valid(token) {
		t.Fatal("freshly created session should be valid")
	}
	if sm.Valid("not-a-real-token") || sm.Valid("") {
		t.Fatal("unknown token should not be valid")
	}
	// A new manager over the same store is what a restart looks like.
	if !NewSessionManager(store).Valid(token) {
		t.Error("session did not survive a restart")
	}
	sm.Revoke(token)
	if NewSessionManager(store).Valid(token) {
		t.Fatal("revoked session should no longer be valid")
	}
}

func TestSessionManagerExpiry(t *testing.T) {
	sm := NewSessionManager(&memStore{})
	sm.ttl = 20 * time.Millisecond
	token, err := sm.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if sm.Valid(token) {
		t.Fatal("expired session should not be valid")
	}
}
