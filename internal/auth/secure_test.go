package auth

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestSecureRequest(t *testing.T) {
	plain := httptest.NewRequest("GET", "http://192.168.1.10:8080/", nil)
	if secureRequest(plain) {
		t.Error("plain HTTP on a LAN must not get a Secure cookie, or login breaks")
	}

	proxied := httptest.NewRequest("GET", "http://autothrottle/", nil)
	proxied.Header.Set("X-Forwarded-Proto", "HTTPS")
	if !secureRequest(proxied) {
		t.Error("behind a TLS-terminating proxy the cookie must be Secure")
	}

	direct := httptest.NewRequest("GET", "https://autothrottle/", nil)
	direct.TLS = &tls.ConnectionState{}
	if !secureRequest(direct) {
		t.Error("a direct TLS connection must get a Secure cookie")
	}
}
