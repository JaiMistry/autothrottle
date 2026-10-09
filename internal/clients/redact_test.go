package clients

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A dropped connection must not write the token into the log, the
// event list or the Dashboard: Go puts the whole URL in the error.
func TestWebSocketErrorsCarryNoToken(t *testing.T) {
	const secret = "SECRET-TOKEN-1234"
	plex := &PlexWatcher{BaseURL: "http://127.0.0.1:1", Token: secret}
	emby := NewEmbyWatcher("http://127.0.0.1:1", secret, false)
	jellyfin := NewJellyfinWatcher("http://127.0.0.1:1", secret, false)
	for name, connect := range map[string]func(context.Context) error{
		"plex": plex.connectAndListen, "emby": emby.connectAndListen, "jellyfin": jellyfin.connectAndListen,
	} {
		err := connect(context.Background())
		if err == nil {
			t.Fatalf("%s: dialing a closed port succeeded", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s leaks its token: %v", name, err)
		}
		if !strings.Contains(err.Error(), "REDACTED") && !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Errorf("%s: the error no longer says what failed: %v", name, err)
		}
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		`Get "ws://h/:/websockets/notifications?X-Plex-Token=abc123": refused`: `Get "ws://h/:/websockets/notifications?X-Plex-Token=REDACTED": refused`,
		`Get "ws://h/socket?api_key=k%2Fy&deviceId=d": refused`:                `Get "ws://h/socket?api_key=REDACTED&deviceId=d": refused`,
		`dial tcp 10.0.0.4:8080: i/o timeout`:                                  `dial tcp 10.0.0.4:8080: i/o timeout`,
	} {
		if got := Redact(errors.New(in)).Error(); got != want {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
	if Redact(nil) != nil {
		t.Error("Redact(nil) is not nil")
	}
}
