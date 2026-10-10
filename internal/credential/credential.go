// Package credential seals a downloader's username and password for
// storage, and opens them again for the code that talks to the client.
package credential

import (
	"encoding/json"
	"fmt"

	"github.com/JaiMistry/autothrottle/internal/crypto"
)

type pair struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Encrypt seals a username and password for storage.
func Encrypt(key []byte, username, password string) (string, error) {
	b, err := json.Marshal(pair{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	return crypto.Encrypt(key, string(b))
}

// Decrypt opens a stored credential. An empty one is not an error: a
// client may run without authentication.
func Decrypt(key []byte, encrypted string) (username, password string, err error) {
	if encrypted == "" {
		return "", "", nil
	}
	raw, err := crypto.Decrypt(key, encrypted)
	if err != nil {
		return "", "", fmt.Errorf("decrypting credential: %w", err)
	}
	var c pair
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return "", "", fmt.Errorf("decoding credential: %w", err)
	}
	return c.Username, c.Password, nil
}
