package auth

import (
	"testing"
	"time"
)

func TestSessionManager(t *testing.T) {
	sm := NewSessionManager(50 * time.Millisecond)

	token, err := sm.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !sm.Valid(token) {
		t.Fatal("freshly created session should be valid")
	}
	if sm.Valid("not-a-real-token") {
		t.Fatal("unknown token should not be valid")
	}

	sm.Revoke(token)
	if sm.Valid(token) {
		t.Fatal("revoked session should no longer be valid")
	}
}

func TestSessionManagerExpiry(t *testing.T) {
	sm := NewSessionManager(20 * time.Millisecond)
	token, err := sm.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	time.Sleep(40 * time.Millisecond)
	if sm.Valid(token) {
		t.Fatal("expired session should not be valid")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if l.Allow() {
		t.Fatal("4th attempt within the window should be blocked")
	}
}

func TestLoginLimiterWindowSlides(t *testing.T) {
	l := NewLoginLimiter(1, 20*time.Millisecond)

	if !l.Allow() {
		t.Fatal("first attempt should be allowed")
	}
	if l.Allow() {
		t.Fatal("second attempt within the window should be blocked")
	}
	time.Sleep(30 * time.Millisecond)
	if !l.Allow() {
		t.Fatal("attempt after the window elapses should be allowed again")
	}
}

func TestCSRF(t *testing.T) {
	csrf, err := NewCSRF()
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}

	token := csrf.TokenFor("session-abc")
	if !csrf.Valid("session-abc", token) {
		t.Fatal("token generated for a session should validate against it")
	}
	if csrf.Valid("session-xyz", token) {
		t.Fatal("token for one session should not validate against a different session")
	}
	if csrf.Valid("session-abc", "garbage") {
		t.Fatal("garbage token should not validate")
	}
}
