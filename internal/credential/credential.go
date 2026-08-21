// Package credential holds the JSON shape used for downloader
// credentials (a username/password pair) before AES-GCM encryption.
// Shared between internal/web (encodes on save) and internal/scheduler
// (decodes to actually authenticate against the downloader) so both
// sides agree on the wire format without either importing the other.
package credential

import (
	"encoding/json"
	"fmt"

	"github.com/JaiMistry/autothrottle/internal/crypto"
)

type Downloader struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func EncodeDownloader(username, password string) (string, error) {
	b, err := json.Marshal(Downloader{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func DecodeDownloader(raw string) (Downloader, error) {
	var c Downloader
	err := json.Unmarshal([]byte(raw), &c)
	return c, err
}

// Decrypt turns a downloader's stored credential blob into the
// username and password its client needs.
//
// An empty blob is not an error: a download client can be configured
// without authentication (qBittorrent bypassing auth for local
// clients, Transmission's RPC left open), and callers then pass empty
// strings through to the client. Every consumer needs exactly this —
// the scheduler applying limits, the sampler reading rates, the web
// layer testing a connection, and the health check — so it lives here
// rather than being written out four times with four slightly
// different error strings.
func Decrypt(key []byte, encrypted string) (username, password string, err error) {
	if encrypted == "" {
		return "", "", nil
	}
	raw, err := crypto.Decrypt(key, encrypted)
	if err != nil {
		return "", "", fmt.Errorf("decrypting credential: %w", err)
	}
	c, err := DecodeDownloader(raw)
	if err != nil {
		return "", "", fmt.Errorf("decoding credential: %w", err)
	}
	return c.Username, c.Password, nil
}
