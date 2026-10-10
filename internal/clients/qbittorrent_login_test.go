package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 4.x answers a good login with 200 "Ok.", 5.x with 204 and no body.
func TestQbittorrentLoginAcceptsBothSuccessShapes(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"4.x success", http.StatusOK, "Ok.", false},
		{"5.x success", http.StatusNoContent, "", false},
		{"bad credentials", http.StatusOK, "Fails.", true},
		{"banned", http.StatusForbidden, "", true},
		{"something else", http.StatusInternalServerError, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/auth/login" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: "SID", Value: "abc"})
				w.WriteHeader(c.status)
				if c.body != "" {
					_, _ = w.Write([]byte(c.body))
				}
			}))
			defer srv.Close()

			_, err := qbittorrentLogin(context.Background(), srv.URL, "u", "p")
			if c.wantErr && err == nil {
				t.Fatalf("want an error, got none")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("want success, got %v", err)
			}
		})
	}
}
