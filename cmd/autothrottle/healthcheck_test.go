//go:build !windows

package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	var stderr bytes.Buffer
	if code := healthcheck(":"+port, &stderr); code != 0 {
		t.Errorf("a serving app: exit %d, %s", code, stderr.String())
	}
	status = http.StatusServiceUnavailable
	if code := healthcheck(":"+port, &stderr); code != 1 {
		t.Errorf("a failing answer: exit %d", code)
	}
	srv.Close()
	if code := healthcheck(":"+port, &stderr); code != 1 {
		t.Errorf("nothing listening: exit %d", code)
	}
}
