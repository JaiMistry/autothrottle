// Package auth handles login: env-var credentials, sessions, CSRF, and
// login rate-limiting.
//
// Credentials come from the environment rather than from a first-run
// setup flow with a stored password hash. For a single-admin app
// deployed from a compose file the env var is already the deployment's
// source of truth, and a setup flow would add an unauthenticated
// bootstrap route and a password-reset path to keep safe — both real
// attack surface, for one account whose owner controls the host
// anyway.
package auth

import (
	"crypto/subtle"
	"errors"
	"os"
)

// Config holds the auth credentials sourced from the environment at
// startup. There is no runtime credential-setting flow — auth.md's
// design deliberately moved that off the original app's first-run web
// wizard and onto deploy-time env vars.
type Config struct {
	Disabled bool
	Username string
	Password string // held in memory only, never persisted
}

// LoadConfig reads AUTH_DISABLED, AUTH_USERNAME,
// and AUTH_PASSWORD. Unless disabled, both username and
// password are required.
func LoadConfig() (Config, error) {
	cfg := Config{
		Disabled: os.Getenv("AUTH_DISABLED") == "true",
		Username: os.Getenv("AUTH_USERNAME"),
		Password: os.Getenv("AUTH_PASSWORD"),
	}
	if cfg.Disabled {
		return cfg, nil
	}
	if cfg.Username == "" || cfg.Password == "" {
		return cfg, errors.New("AUTH_USERNAME and AUTH_PASSWORD are required unless AUTH_DISABLED=true")
	}
	return cfg, nil
}

// Verify checks credentials in constant time. Credentials live only as
// plaintext in memory (sourced fresh from the environment each start,
// never persisted) — bcrypt would add per-login CPU cost without
// protecting anything a stored hash would, since there's no hash to
// protect from a database leak here. Constant-time comparison is what
// actually matters: resisting a timing attack against the live value.
func (c Config) Verify(username, password string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(c.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(c.Password)) == 1
	return userOK && passOK
}
