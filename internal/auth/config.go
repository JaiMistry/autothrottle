// Package auth handles login: who may sign in, sessions, CSRF, and
// login rate-limiting.
//
// There are four ways an instance can be set up, checked in this order:
//
//   - AUTH_DISABLED=true — no login at all, for an instance that sits
//     behind its own authenticating proxy.
//   - AUTH_USERNAME and AUTH_PASSWORD — credentials from the
//     environment. They always win, which also makes them the recovery
//     path for anyone locked out of a stored account.
//   - A stored account, created once on the first-run setup screen.
//   - Nothing yet — every page leads to setup.
//
// The setup screen is the one page anyone can reach without logging
// in, and whoever reaches it first would own the instance. It is closed
// to strangers by a one-time setup code that is only ever printed to
// the container log: completing setup needs access to the host, not
// just to the network.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
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
	ErrBadSetupCode  = errors.New("that setup code is not right — copy it from the container log")
	ErrUsernameEmpty = errors.New("choose a username")
	ErrPasswordShort = fmt.Errorf("use a password of at least %d characters", minPasswordLength)
)

const minPasswordLength = 8

// Authenticator decides who may log in. Unlike the environment, the
// stored account can appear while the app is running — setup creates
// it — so this is consulted per request rather than read once.
type Authenticator struct {
	disabled bool
	envUser  string
	envPass  string // held in memory only, never persisted
	store    AccountStore

	mu        sync.Mutex
	setupCode string
}

// Load reads AUTH_DISABLED, AUTH_USERNAME and AUTH_PASSWORD. Setting
// only one of the pair is refused rather than half-applied: it is
// always a mistake, and silently falling through to the setup screen
// would hide it.
func Load(store AccountStore) (*Authenticator, error) {
	a := &Authenticator{
		disabled: os.Getenv("AUTH_DISABLED") == "true",
		envUser:  os.Getenv("AUTH_USERNAME"),
		envPass:  os.Getenv("AUTH_PASSWORD"),
		store:    store,
	}
	if !a.disabled && (a.envUser == "") != (a.envPass == "") {
		return nil, errors.New("AUTH_USERNAME and AUTH_PASSWORD must be set together — set both, or neither to create an account on first run")
	}
	if a.Mode() == ModeSetup {
		code, err := newSetupCode()
		if err != nil {
			return nil, err
		}
		a.setupCode = code
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

// SetupCode is the one-time code the setup screen asks for, or "" once
// there is no setup left to do.
func (a *Authenticator) SetupCode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Mode() != ModeSetup {
		return ""
	}
	return a.setupCode
}

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

// Verify checks a login attempt against whichever credentials apply.
// Both halves are always compared, in constant time, so the response
// time reveals nothing about which half was wrong.
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

// CompleteSetup creates the account. The code is checked first, so a
// wrong one says nothing about whether the other fields would have been
// accepted.
func (a *Authenticator) CompleteSetup(code, username, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.Mode() != ModeSetup {
		return ErrSetupDone
	}
	if subtle.ConstantTimeCompare([]byte(normalizeCode(code)), []byte(normalizeCode(a.setupCode))) != 1 {
		return ErrBadSetupCode
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return ErrUsernameEmpty
	}
	if len(password) < minPasswordLength {
		return ErrPasswordShort
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if err := a.store.CreateAccount(username, hash); err != nil {
		return err
	}
	a.setupCode = ""
	return nil
}

// setupAlphabet leaves out characters that are easy to misread when
// copying from a terminal: 0/O, 1/I/L.
const setupAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// newSetupCode returns eight characters as XXXX-XXXX: about 39 bits,
// plenty for a code that is rate-limited and lives only until setup.
func newSetupCode() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating setup code: %w", err)
	}
	var b strings.Builder
	for i, v := range raw {
		if i == 4 {
			b.WriteByte('-')
		}
		b.WriteByte(setupAlphabet[int(v)%len(setupAlphabet)])
	}
	return b.String(), nil
}

// normalizeCode forgives how a code was copied: case, and the dash or
// any spacing around it.
func normalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, strings.ToUpper(code))
}
