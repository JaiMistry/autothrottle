package web

import "encoding/json"

// downloaderCredential is JSON-encoded then AES-GCM encrypted as a
// single blob before storage, since a downloader (unlike a media
// server's single token) needs a username/password pair. Keeps the
// schema's one credential_encrypted TEXT column uniform across both
// tables rather than adding downloader-specific columns.
type downloaderCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func encodeDownloaderCredential(username, password string) (string, error) {
	b, err := json.Marshal(downloaderCredential{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeDownloaderCredential(raw string) (downloaderCredential, error) {
	var c downloaderCredential
	err := json.Unmarshal([]byte(raw), &c)
	return c, err
}
