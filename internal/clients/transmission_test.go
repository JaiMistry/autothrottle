package clients

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type transmissionStub struct {
	requests  []transmissionRequest
	sessionID []string // session id header seen on each request
	authUser  []string
	hadAuth   []bool
	paths     []string
}

// newTransmissionStub demands the session-id handshake once, then answers
// with arguments.
func newTransmissionStub(t *testing.T, arguments string) (*transmissionStub, *httptest.Server) {
	t.Helper()
	stub := &transmissionStub{}
	const wantSession = "abc123"

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req transmissionRequest
		_ = json.Unmarshal(body, &req)

		user, _, hadAuth := r.BasicAuth()
		stub.requests = append(stub.requests, req)
		stub.sessionID = append(stub.sessionID, r.Header.Get(transmissionSessionHeader))
		stub.authUser = append(stub.authUser, user)
		stub.hadAuth = append(stub.hadAuth, hadAuth)
		stub.paths = append(stub.paths, r.URL.Path)

		if r.Header.Get(transmissionSessionHeader) != wantSession {
			rw.Header().Set(transmissionSessionHeader, wantSession)
			rw.WriteHeader(http.StatusConflict)
			return
		}
		_, _ = rw.Write([]byte(`{"result":"success","arguments":` + arguments + `}`))
	}))
	t.Cleanup(srv.Close)
	return stub, srv
}

// The first 409 is step one of the handshake, not an error.
func TestTransmissionCompletesSessionHandshake(t *testing.T) {
	stub, srv := newTransmissionStub(t, `{}`)

	if err := TestTransmission(context.Background(), srv.URL, "", ""); err != nil {
		t.Fatalf("TestTransmission: %v", err)
	}

	if len(stub.requests) != 2 {
		t.Fatalf("made %d requests, want 2 (the 409 then the retry)", len(stub.requests))
	}
	if stub.sessionID[0] != "" {
		t.Errorf("first request already carried a session id: %q", stub.sessionID[0])
	}
	if stub.sessionID[1] != "abc123" {
		t.Errorf("retry sent session id %q, want the one from the 409", stub.sessionID[1])
	}
	if stub.requests[1].Method != "session-get" {
		t.Errorf("method = %q, want session-get", stub.requests[1].Method)
	}
	if stub.paths[1] != transmissionRPCPath {
		t.Errorf("posted to %q, want %q", stub.paths[1], transmissionRPCPath)
	}
}

func TestTransmissionGivesUpOnRepeated409(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		calls++
		rw.Header().Set(transmissionSessionHeader, "never-accepted")
		rw.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	err := TestTransmission(context.Background(), srv.URL, "", "")
	if err == nil {
		t.Fatal("expected an error when the session id is never accepted")
	}
	if calls > 2 {
		t.Errorf("made %d requests; must not retry indefinitely", calls)
	}
}

func TestTransmission409WithoutHeaderIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	err := TestTransmission(context.Background(), srv.URL, "", "")
	if err == nil || !strings.Contains(err.Error(), transmissionSessionHeader) {
		t.Fatalf("error = %v, want it to name the missing header", err)
	}
}

// Transmission reports failure in the body of a 200.
func TestTransmissionTreatsNonSuccessResultAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"result":"method name not recognized","arguments":{}}`))
	}))
	defer srv.Close()

	err := TestTransmission(context.Background(), srv.URL, "", "")
	if err == nil {
		t.Fatal("a 200 carrying a failure result must be an error")
	}
	if !strings.Contains(err.Error(), "method name not recognized") {
		t.Errorf("error = %v, want it to carry the server's reason", err)
	}
}

func TestTransmissionStatusHandling(t *testing.T) {
	cases := []struct {
		status  int
		wantMsg string
	}{
		{http.StatusUnauthorized, "credentials were rejected"},
		{http.StatusNotFound, "no RPC endpoint"},
		{http.StatusInternalServerError, "unexpected status"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			rw.WriteHeader(c.status)
		}))
		err := TestTransmission(context.Background(), srv.URL, "u", "p")
		srv.Close()

		if err == nil {
			t.Errorf("status %d: expected an error", c.status)
			continue
		}
		if !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("status %d: error %q does not mention %q", c.status, err, c.wantMsg)
		}
	}
}

func TestTransmissionSendsBasicAuthOnlyWhenSet(t *testing.T) {
	stub, srv := newTransmissionStub(t, `{}`)
	if err := TestTransmission(context.Background(), srv.URL, "alice", "hunter2"); err != nil {
		t.Fatalf("with credentials: %v", err)
	}
	if !stub.hadAuth[0] || stub.authUser[0] != "alice" {
		t.Errorf("credentials were not sent: hadAuth=%v user=%q", stub.hadAuth[0], stub.authUser[0])
	}

	stub2, srv2 := newTransmissionStub(t, `{}`)
	if err := TestTransmission(context.Background(), srv2.URL, "", ""); err != nil {
		t.Fatalf("without credentials: %v", err)
	}
	if stub2.hadAuth[0] {
		t.Error("sent a Basic auth header with no credentials configured")
	}
}

func TestSetTransmissionLimitsEnablesAndDisables(t *testing.T) {
	stub, srv := newTransmissionStub(t, `{}`)

	if err := SetTransmissionLimits(context.Background(), srv.URL, "", "", 500, 100); err != nil {
		t.Fatalf("SetTransmissionLimits: %v", err)
	}

	req := stub.requests[len(stub.requests)-1]
	if req.Method != "session-set" {
		t.Fatalf("method = %q, want session-set", req.Method)
	}
	args, ok := req.Arguments.(map[string]any)
	if !ok {
		t.Fatalf("arguments decoded as %T", req.Arguments)
	}
	if args["speed-limit-down"] != float64(500) || args["speed-limit-up"] != float64(100) {
		t.Errorf("limits = down %v up %v, want 500/100", args["speed-limit-down"], args["speed-limit-up"])
	}
	if args["speed-limit-down-enabled"] != true || args["speed-limit-up-enabled"] != true {
		t.Errorf("limits were not enabled: %v", args)
	}

	// 0 means unlimited: a zero limit left enabled would stop all traffic.
	stub2, srv2 := newTransmissionStub(t, `{}`)
	if err := SetTransmissionLimits(context.Background(), srv2.URL, "", "", 0, 0); err != nil {
		t.Fatalf("SetTransmissionLimits(0,0): %v", err)
	}
	args, _ = stub2.requests[len(stub2.requests)-1].Arguments.(map[string]any)
	if args["speed-limit-down-enabled"] != false || args["speed-limit-up-enabled"] != false {
		t.Errorf("zero must disable the limit, not set it to zero: %v", args)
	}
}

func TestDownloaderResolvers(t *testing.T) {
	for _, dlType := range []string{"qbittorrent", "transmission"} {
		if !SupportedDownloaderType(dlType) {
			t.Errorf("SupportedDownloaderType(%q) = false", dlType)
		}
		if RateLimiterFor(dlType) == nil {
			t.Errorf("RateLimiterFor(%q) = nil", dlType)
		}
	}

	if SupportedDownloaderType("deluge") {
		t.Error(`SupportedDownloaderType("deluge") = true, want false`)
	}
	if RateLimiterFor("deluge") != nil {
		t.Error("RateLimiterFor for an unknown type must be nil so callers can report it")
	}
	if err := TestDownloader(context.Background(), "deluge", "http://x", "", ""); err == nil {
		t.Error("TestDownloader for an unknown type should return an error")
	}
}
