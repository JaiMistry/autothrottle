// Package auth handles login: who may sign in, sessions, CSRF and rate
// limiting. Modes, in order of precedence: AUTH_DISABLED (behind an
// authenticating proxy); AUTH_USERNAME/AUTH_PASSWORD, which always win
// and so also recover a lost account; the stored account from first-run
// setup; and nothing yet, when every page leads to setup.
package auth

import (
	"crypto/subtle"
	"errors"
	"os"
	"strings"
	"sync"
)

// AccountStore persists the single account created during setup.
type AccountStore interface {
	GetAccount() (username, passwordHash string, ok bool, err error)
	CreateAccount(username, passwordHash string) error
}

// Mode is how this instance decides who may log in.
type Mode int

const (
	ModeSetup       Mode = iota // no credentials anywhere yet
	ModeAccount                 // the account created on the setup screen
	ModeEnvironment             // AUTH_USERNAME / AUTH_PASSWORD
	ModeDisabled                // AUTH_DISABLED=true
)

// Setup failures the screen can explain.
var (
	ErrSetupDone     = errors.New("setup has already been completed")
	ErrUsernameEmpty = errors.New("choose a username")
	// No password rules: any non-empty password is accepted.
	ErrPasswordEmpty = errors.New("choose a password")
)

// Authenticator decides who may log in, per request: setup can create
// the account while the app runs.
type Authenticator struct {
	disabled bool
	envUser  string
	envPass  string // held in memory only, never persisted
	store    AccountStore

	// mu makes setup happen once.
	mu sync.Mutex
}

// Load reads AUTH_DISABLED, AUTH_USERNAME and AUTH_PASSWORD. Only one
// of the pair set is an error, not a fall-through to setup.
func Load(store AccountStore) (*Authenticator, error) {
	a := &Authenticator{
		disabled: os.Getenv("AUTH_DISABLED") == "true",
		envUser:  os.Getenv("AUTH_USERNAME"),
		envPass:  os.Getenv("AUTH_PASSWORD"),
		store:    store,
	}
	if !a.disabled && (a.envUser == "") != (a.envPass == "") {
		return nil, errors.New("AUTH_USERNAME and AUTH_PASSWORD must be set together: set both, or neither to create an account on first run")
	}
	return a, nil
}

// Mode reports how logins are decided right now.
func (a *Authenticator) Mode() Mode {
	switch {
	case a.disabled:
		return ModeDisabled
	case a.envUser != "":
		return ModeEnvironment
	}
	if _, _, ok, err := a.store.GetAccount(); err == nil && ok {
		return ModeAccount
	}
	return ModeSetup
}

// Disabled reports whether login is switched off entirely.
func (a *Authenticator) Disabled() bool { return a.disabled }

// NeedsSetup reports whether nobody can log in until an account is made.
func (a *Authenticator) NeedsSetup() bool { return a.Mode() == ModeSetup }

// Username is who is signed in, for display.
func (a *Authenticator) Username() string {
	if a.envUser != "" {
		return a.envUser
	}
	if user, _, ok, err := a.store.GetAccount(); err == nil && ok {
		return user
	}
	return ""
}

// Verify checks a login attempt, comparing both halves in constant time.
func (a *Authenticator) Verify(username, password string) bool {
	switch a.Mode() {
	case ModeEnvironment:
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(a.envUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.envPass)) == 1
		return userOK && passOK
	case ModeAccount:
		storedUser, hash, _, err := a.store.GetAccount()
		if err != nil {
			return false
		}
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(storedUser)) == 1
		passOK := checkPassword(hash, password)
		return userOK && passOK
	default:
		return false
	}
}

// CompleteSetup creates the account.
func (a *Authenticator) CompleteSetup(username, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.Mode() != ModeSetup {
		return ErrSetupDone
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return ErrUsernameEmpty
	}
	if password == "" {
		return ErrPasswordEmpty
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return a.store.CreateAccount(username, hash)
}
