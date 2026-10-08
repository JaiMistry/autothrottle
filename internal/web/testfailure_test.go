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
		{"no route blames the address", probeServer, errors.New("dial tcp 192.168.1.99:32400: connect: no route to host"), fieldURL, "Nothing answered at that address"},
		{"timeout blames reachability", probeServer, errors.New("dial tcp: i/o timeout"), fieldURL, "unreachable from the AutoThrottle container"},
		{"timeout quotes the real limit", probeServer, errors.New("context deadline exceeded"), fieldURL, "No reply in 8s"},
		{"401 blames the credential", probeServer, errors.New("unexpected status 401 Unauthorized"), fieldCredential, "credential was rejected"},
		{"403 on a downloader names both fields", probeDownloader, errors.New("403 Forbidden"), fieldCredential, "username or password"},
		{"tls points at the URL", probeServer, errors.New("x509: certificate signed by unknown authority"), fieldURL, "TLS handshake failed"},
		{"dns points at the URL", probeServer, errors.New("dial tcp: lookup plex.example: no such host"), fieldURL, "did not resolve"},
		{"a scheme-less URL is a URL problem", probeServer, errors.New(`Get "192.168.1.50:32400": unsupported protocol scheme ""`), fieldURL, "not a complete URL"},
		{"nil is not a failure", probeServer, nil, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fields, hint := classifyProbeError(c.kind, c.err)
			got := ""
			if len(fields) > 0 {
				got = fields[0]
			}
			if got != c.wantField {
				t.Errorf("field = %q, want %q", got, c.wantField)
			}
			if c.wantIn != "" && !strings.Contains(hint, c.wantIn) {
				t.Errorf("hint = %q, want it to contain %q", hint, c.wantIn)
			}
		})
	}
}

// Every blank is named at once, so fixing one does not just surface the
// next on the following attempt.
func TestMissingRequiredNamesEveryBlank(t *testing.T) {
	fields, hint := missingRequired("", "", "", true)
	if len(fields) != 3 {
		t.Fatalf("want name, url and credential, got %v", fields)
	}
	for _, want := range []string{"a name", "a URL", "a credential"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q is missing %q", hint, want)
		}
	}

	// A downloader does not require credentials at all.
	if fields, _ := missingRequired("qb", "http://x", "", false); fields != nil {
		t.Errorf("a named downloader with a URL needs nothing else, got %v", fields)
	}
	// A credential already stored counts as present.
	if fields, _ := missingRequired("plex", "http://x", "stored-token", true); fields != nil {
		t.Errorf("a supplied credential should satisfy the check, got %v", fields)
	}
}

// An error nobody anticipated must not be given invented advice: the raw
// text is still shown, and no field is outlined.
func TestClassifyProbeErrorStaysQuietWhenUnsure(t *testing.T) {
	fields, hint := classifyProbeError(probeServer, errors.New("something nobody has seen before"))
	if len(fields) != 0 || hint != "" {
		t.Fatalf("want no field and no hint, got %v / %q", fields, hint)
	}
}
