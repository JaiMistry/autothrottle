package credential

import (
	"testing"

	"github.com/JaiMistry/autothrottle/internal/crypto"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func TestRoundTrip(t *testing.T) {
	// A password with every character JSON would trip over if the
	// encoding were done by hand.
	sealed, err := Encrypt(key, "admin", `p"a\ss:wörd`)
	if err != nil {
		t.Fatal(err)
	}
	user, pass, err := Decrypt(key, sealed)
	if err != nil || user != "admin" || pass != `p"a\ss:wörd` {
		t.Fatalf("Decrypt = %q %q %v", user, pass, err)
	}
}

// A client with authentication switched off has no stored credential,
// and that must read as empty rather than as a failure.
func TestNoCredentialIsNotAnError(t *testing.T) {
	user, pass, err := Decrypt(key, "")
	if err != nil || user != "" || pass != "" {
		t.Fatalf("Decrypt(\"\") = %q %q %v", user, pass, err)
	}
}

func TestUnreadableCredentialIsAnError(t *testing.T) {
	key := make([]byte, 32)
	notJSON, err := crypto.Encrypt(key, "not json")
	if err != nil {
		t.Fatal(err)
	}
	for name, stored := range map[string]string{"undecryptable": "%%%", "not a credential": notJSON} {
		if _, _, err := Decrypt(key, stored); err == nil {
			t.Errorf("%s: decrypted", name)
		}
	}
}
