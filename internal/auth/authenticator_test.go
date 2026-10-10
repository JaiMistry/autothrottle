package auth

import (
	"errors"
	"sync"
	"testing"
)

// accountMem is an AccountStore with the same once-only rule the database
// enforces.
type accountMem struct {
	mu         sync.Mutex
	user, hash string
	ok         bool
}

func (m *accountMem) GetAccount() (string, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.user, m.hash, m.ok, nil
}

func (m *accountMem) CreateAccount(user, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ok {
		return errors.New("an account already exists")
	}
	m.user, m.hash, m.ok = user, hash, true
	return nil
}

func load(t *testing.T, disabled, user, pass string, store *accountMem) (*Authenticator, error) {
	t.Helper()
	t.Setenv("AUTH_DISABLED", disabled)
	t.Setenv("AUTH_USERNAME", user)
	t.Setenv("AUTH_PASSWORD", pass)
	return Load(store)
}

func TestModes(t *testing.T) {
	cases := []struct {
		name, disabled, user, pass string
		account                    bool
		want                       Mode
	}{
		{"disabled wins over everything", "true", "admin", "pw", true, ModeDisabled},
		{"environment wins over a stored account", "", "admin", "pw", true, ModeEnvironment},
		{"stored account", "", "", "", true, ModeAccount},
		{"nothing yet", "", "", "", false, ModeSetup},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &accountMem{}
			if c.account {
				h, _ := hashPassword("stored-password")
				_ = store.CreateAccount("stored", h)
			}
			a, err := load(t, c.disabled, c.user, c.pass, store)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := a.Mode(); got != c.want {
				t.Errorf("Mode() = %v, want %v", got, c.want)
			}
		})
	}
}

// Half a credential pair is always a mistake; quietly falling through
// to the setup screen would hide it.
func TestHalfACredentialPairIsRefused(t *testing.T) {
	if _, err := load(t, "", "admin", "", &accountMem{}); err == nil {
		t.Error("AUTH_USERNAME without AUTH_PASSWORD should be refused")
	}
	if _, err := load(t, "", "", "pw", &accountMem{}); err == nil {
		t.Error("AUTH_PASSWORD without AUTH_USERNAME should be refused")
	}
}

func TestEnvironmentVerify(t *testing.T) {
	a, _ := load(t, "", "admin", "correct-horse", &accountMem{})
	if !a.Verify("admin", "correct-horse") {
		t.Error("right credentials rejected")
	}
	if a.Verify("admin", "wrong") || a.Verify("nope", "correct-horse") {
		t.Error("wrong credentials accepted")
	}
}

func TestSetupCreatesAnAccountOnce(t *testing.T) {
	store := &accountMem{}
	a, _ := load(t, "", "", "", store)
	if err := a.CompleteSetup("  ", "a-long-password"); !errors.Is(err, ErrUsernameEmpty) {
		t.Fatalf("blank username: err = %v", err)
	}
	if err := a.CompleteSetup("jai", ""); !errors.Is(err, ErrPasswordEmpty) {
		t.Fatalf("empty password: err = %v", err)
	}

	if err := a.CompleteSetup(" jai ", "a-long-password"); err != nil {
		t.Fatalf("CompleteSetup: %v", err)
	}
	if a.Mode() != ModeAccount || a.Username() != "jai" {
		t.Fatalf("after setup: mode %v user %q", a.Mode(), a.Username())
	}
	if !a.Verify("jai", "a-long-password") || a.Verify("jai", "a-long-passwore") {
		t.Error("stored account does not verify correctly")
	}
	if err := a.CompleteSetup("intruder", "another-password"); !errors.Is(err, ErrSetupDone) {
		t.Errorf("second setup: err = %v, want ErrSetupDone", err)
	}
}

func TestPasswordHash(t *testing.T) {
	h1, _ := hashPassword("hunter2hunter2")
	h2, _ := hashPassword("hunter2hunter2")
	if h1 == h2 {
		t.Error("two hashes of one password must differ — the salt is missing")
	}
	if !checkPassword(h1, "hunter2hunter2") || checkPassword(h1, "hunter2hunter3") {
		t.Error("checkPassword gives the wrong answer")
	}
	for _, junk := range []string{"", "plain", "pbkdf2-sha256$x$y$z", "md5$1$a$b"} {
		if checkPassword(junk, "anything") {
			t.Errorf("an unreadable hash %q must never match", junk)
		}
	}
}
