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
		{"refused, as Windows says it", probeServer, errors.New("dial tcp 10.0.0.1:32400: connectex: No connection could be made because the target machine actively refused it."), fieldURL, "Nothing is listening"},
		{"unreachable, as Windows says it", probeServer, errors.New("dial tcp 192.168.9.9:32400: connectex: A socket operation was attempted to an unreachable host."), fieldURL, "Nothing answered at that address"},
		{"no route blames the address", probeServer, errors.New("dial tcp 192.168.1.99:32400: connect: no route to host"), fieldURL, "Nothing answered at that address"},
		{"timeout blames reachability", probeServer, errors.New("dial tcp: i/o timeout"), fieldURL, "unreachable from " + host},
		{"timeout quotes the real limit", probeServer, errors.New("context deadline exceeded"), fieldURL, "No reply in 8s"},
		{"401 blames the credential", probeServer, errors.New("unexpected status 401 Unauthorized"), fieldCredential, "credential was rejected"},
		{"403 on a downloader names both fields", probeDownloader, errors.New("403 Forbidden"), fieldCredential, "username or password"},
		{"tls points at the URL", probeServer, errors.New("x509: certificate signed by unknown authority"), fieldURL, "TLS handshake failed"},
		{"dns points at the URL", probeServer, errors.New("dial tcp: lookup plex.example: no such host"), fieldURL, "did not resolve"},
		{"a scheme-less URL is a URL problem", probeServer, errors.New(`Get "192.168.1.50:32400": unsupported protocol scheme ""`), fieldURL, "not a complete URL"},
		{"a redirect elsewhere points at the URL", probeServer, errors.New(`Get "http://plex.lan:32400/": redirected to another address, https://auth.example, which was not followed`), fieldURL, "redirected to a different address"},
		{"a redirect is not mistaken for a 401", probeDownloader, errors.New(`Post "http://qbit.lan:8080/api/v2/auth/login": redirected to another address, http://401.example, which was not followed`), fieldURL, "redirected to a different address"},
		{"nil is not a failure", probeServer, nil, "", ""},
		// Nothing anticipated: the raw error shows, without invented advice.
		{"unknown errors get no advice", probeServer, errors.New("something nobody has seen before"), "", ""},
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
			if !strings.Contains(hint, c.wantIn) || (c.wantIn == "" && hint != "") {
				t.Errorf("hint = %q, want it to contain %q", hint, c.wantIn)
			}
		})
	}
}

// Every blank is named at once, not one per attempt.
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

func TestHostFor(t *testing.T) {
	if got := hostFor("windows"); got != "this PC" {
		t.Errorf("windows: %q", got)
	}
	if got := hostFor("linux"); got != "the AutoThrottle container" {
		t.Errorf("linux: %q", got)
	}
}
