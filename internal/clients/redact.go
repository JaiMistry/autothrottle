package clients

import (
	"errors"
	"regexp"
)

// secretParam matches a credential carried in a URL's query string.
// Plex and Emby/Jellyfin both take their token that way on the
// WebSocket, the one place a header cannot carry it, and Go writes the
// whole URL into a failed request's error.
var secretParam = regexp.MustCompile(`(?i)((?:X-Plex-Token|api_key|access_token|token)=)[^&\s"']*`)

// redact replaces any credential in s with a placeholder.
func redact(s string) string {
	return secretParam.ReplaceAllString(s, "${1}REDACTED")
}

// redactedError is an error whose text has had credentials removed. It
// does not unwrap: the original carries the secret.
type redactedError struct{ msg string }

func (e redactedError) Error() string { return e.msg }

// Redact returns err with any credential removed from its text, so it is
// safe to log, store, or show. nil stays nil.
func Redact(err error) error {
	if err == nil {
		return nil
	}
	var already redactedError
	if errors.As(err, &already) {
		return err
	}
	return redactedError{redact(err.Error())}
}
