package settings

import "fmt"

// InvalidError reports a rejected Set — a bad key, or a value outside
// the key's declared bounds. Web handlers surface Reason directly.
type InvalidError struct {
	Key    string
	Reason string
}

func (e *InvalidError) Error() string { return fmt.Sprintf("%s: %s", e.Key, e.Reason) }

func errInvalid(key, reason string) error { return &InvalidError{Key: key, Reason: reason} }
