package web

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyProbeError(t *testing.T) {
	cases := []struct {
		name      string
		kind      probeKind
		err       error
		wantField string
		wantIn    string
	}{
		{"refused names the port", probeServer, errors.New("dial tcp 10.0.0.1:32400: connect: connection refused"), fieldURL, "Nothing is listening"},
		{"refused on a downloader says WebUI", probeDownloader, errors.New("connect: connection refused"), fieldURL, "WebUI port"},
		{"timeout blames reachability", probeServer, errors.New("dial tcp: i/o timeout"), fieldURL, "unreachable from the AutoThrottle container"},
		{"401 blames the credential", probeServer, errors.New("unexpected status 401 Unauthorized"), fieldCredential, "credential was rejected"},
		{"403 on a downloader names both fields", probeDownloader, errors.New("403 Forbidden"), fieldCredential, "username or password"},
		{"tls points at the URL", probeServer, errors.New("x509: certificate signed by unknown authority"), fieldURL, "TLS handshake failed"},
		{"dns points at the URL", probeServer, errors.New("dial tcp: lookup plex.example: no such host"), fieldURL, "did not resolve"},
		{"a scheme-less URL is a URL problem", probeServer, errors.New(`Get "192.168.1.12:32400": unsupported protocol scheme ""`), fieldURL, "not a complete URL"},
		{"nil is not a failure", probeServer, nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			field, hint := classifyProbeError(c.kind, c.err)
			if field != c.wantField {
				t.Errorf("field = %q, want %q", field, c.wantField)
			}
			if c.wantIn != "" && !strings.Contains(hint, c.wantIn) {
				t.Errorf("hint = %q, want it to contain %q", hint, c.wantIn)
			}
		})
	}
}

// An error nobody anticipated must not be given invented advice: the raw
// text is still shown, and no field is outlined.
func TestClassifyProbeErrorStaysQuietWhenUnsure(t *testing.T) {
	field, hint := classifyProbeError(probeServer, errors.New("something nobody has seen before"))
	if field != "" || hint != "" {
		t.Fatalf("want no field and no hint, got %q / %q", field, hint)
	}
}
