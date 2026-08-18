// Package credential holds the JSON shape used for downloader
// credentials (a username/password pair) before AES-GCM encryption.
// Shared between internal/web (encodes on save) and internal/scheduler
// (decodes to actually authenticate against the downloader) so both
// sides agree on the wire format without either importing the other.
package credential

import "encoding/json"

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
