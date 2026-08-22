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

// A restart must not sign everyone out. Sessions held only in memory
// did exactly that, which on a self-hosted app that auto-updates means
// being logged out regularly for no visible reason.
func TestSessionsSurviveARestart(t *testing.T) {
	store := &memStore{}
	first := NewSessionManager(time.Hour).WithStore(store)
	token, err := first.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Valid(token) {
		t.Fatal("a fresh session should be valid")
	}

	// A new manager over the same store is what a restart looks like.
	second := NewSessionManager(time.Hour).WithStore(store)
	if !second.Valid(token) {
		t.Error("session did not survive the restart")
	}
}

func TestRevokeClearsTheDurableCopy(t *testing.T) {
	store := &memStore{}
	m := NewSessionManager(time.Hour).WithStore(store)
	token, _ := m.Create()
	m.Revoke(token)

	if NewSessionManager(time.Hour).WithStore(store).Valid(token) {
		t.Error("a revoked session must not come back after a restart")
	}
}

// An expired token in the durable copy must not be adopted back.
func TestExpiredStoredSessionIsRejected(t *testing.T) {
	store := &memStore{rows: map[string]time.Time{"stale": time.Now().Add(-time.Minute)}}
	if NewSessionManager(time.Hour).WithStore(store).Valid("stale") {
		t.Error("an expired stored session must not be accepted")
	}
}

// Without a store the manager still works, it just forgets on restart.
func TestSessionsWorkWithoutAStore(t *testing.T) {
	m := NewSessionManager(time.Hour)
	token, err := m.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Valid(token) {
		t.Error("in-memory sessions should still validate")
	}
}
