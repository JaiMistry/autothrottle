package web

import (
	"net/http"
	"testing"
)

// Docker's health check asks without signing in, before setup as after,
// and hears nothing but that the app is serving.
func TestHealth(t *testing.T) {
	for _, app := range []*testApp{newAppWith(t, nil), newApp(t)} {
		app.cookies = nil
		rec := app.get("/health")
		expectStatus(t, rec, http.StatusOK)
		if rec.Body.String() != "ok\n" {
			t.Errorf("body = %q", rec.Body.String())
		}
	}
}
