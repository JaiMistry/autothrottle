package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDescribe(t *testing.T) {
	cases := []struct {
		st   status
		err  error
		want string
		up   bool
	}{
		{err: errors.New("refused"), want: "AutoThrottle isn't running"},
		{st: status{Setup: true}, want: "Open AutoThrottle to finish setting up", up: true},
		{st: status{Paused: true, Statement: "Default downloader speeds enforced"}, want: "Throttling is turned off", up: true},
		{st: status{Statement: "Nothing is throttled"}, want: "Nothing is throttled", up: true},
		{st: status{Statement: "qBittorrent throttled", Sessions: 1, Throttled: 1}, want: "qBittorrent throttled · 1 stream playing", up: true},
		{st: status{Statement: "qBittorrent throttled", Sessions: 3, Throttled: 1}, want: "qBittorrent throttled · 3 streams playing", up: true},
	}
	for _, c := range cases {
		got, up := describe(c.st, c.err)
		if got != c.want || up != c.up {
			t.Errorf("describe(%+v, %v) = %q, %v; want %q, %v", c.st, c.err, got, up, c.want, c.up)
		}
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"setup":false,"statement":"Nothing is throttled","sessions":2}`))
		case "/garbled":
			_, _ = w.Write([]byte(`{`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := fetch(srv.Client(), srv.URL+"/ok")
	if err != nil || st.Statement != "Nothing is throttled" || st.Sessions != 2 {
		t.Errorf("ok: %+v, %v", st, err)
	}
	if _, err := fetch(srv.Client(), srv.URL+"/missing"); err == nil {
		t.Error("a 404 read as a status")
	}
	if _, err := fetch(srv.Client(), srv.URL+"/garbled"); err == nil {
		t.Error("broken JSON read as a status")
	}
	srv.Close()
	if _, err := fetch(srv.Client(), srv.URL+"/ok"); err == nil {
		t.Error("a server that is gone read as a status")
	}
}
