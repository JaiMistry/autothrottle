package auth

import (
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	t.Run("disabled skips credential requirement", func(t *testing.T) {
		t.Setenv("AUTH_DISABLED", "true")
		t.Setenv("AUTH_USERNAME", "")
		t.Setenv("AUTH_PASSWORD", "")

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if !cfg.Disabled {
			t.Fatal("expected Disabled to be true")
		}
	})

	t.Run("enabled requires both credentials", func(t *testing.T) {
		t.Setenv("AUTH_DISABLED", "false")
		t.Setenv("AUTH_USERNAME", "")
		t.Setenv("AUTH_PASSWORD", "")

		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error when credentials are missing and auth is enabled")
		}
	})

	t.Run("enabled with credentials succeeds", func(t *testing.T) {
		t.Setenv("AUTH_DISABLED", "false")
		t.Setenv("AUTH_USERNAME", "admin")
		t.Setenv("AUTH_PASSWORD", "hunter2")

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Username != "admin" || cfg.Password != "hunter2" {
			t.Fatal("expected credentials to be loaded from env")
		}
	})
}

func TestConfigVerify(t *testing.T) {
	cfg := Config{Username: "admin", Password: "correct-horse"}

	if !cfg.Verify("admin", "correct-horse") {
		t.Fatal("expected correct credentials to verify")
	}
	if cfg.Verify("admin", "wrong") {
		t.Fatal("expected wrong password to fail")
	}
	if cfg.Verify("nope", "correct-horse") {
		t.Fatal("expected wrong username to fail")
	}
}

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
