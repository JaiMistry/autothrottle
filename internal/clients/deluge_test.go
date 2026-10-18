package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDeluge answers the Web UI's JSON-RPC the way Deluge 2.2 does: a
// session cookie from auth.login, a Web UI that starts disconnected from
// its daemon, and daemon calls that are "Unknown method" until connected.
type fakeDeluge struct {
	mu        sync.Mutex
	password  string
	connected bool
	hosts     [][]any // id, host, port, user
	status    map[string]string
	config    map[string]float64
	methods   []string
}

func newFakeDeluge(t *testing.T) (*fakeDeluge, *httptest.Server) {
	t.Helper()
	f := &fakeDeluge{
		password: "deluge",
		hosts:    [][]any{{"host-1", "127.0.0.1", 58846, "localclient"}},
		status:   map[string]string{"host-1": "Online"},
		config:   map[string]float64{"max_download_speed": -1, "max_upload_speed": -1},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeDeluge) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/json" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
		ID     int               `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods = append(f.methods, req.Method)

	reply := func(result any, errMsg string) {
		var e any
		if errMsg != "" {
			e = map[string]any{"message": errMsg, "code": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": e, "id": req.ID})
	}
	if req.Method == "auth.login" {
		var pw string
		_ = json.Unmarshal(req.Params[0], &pw)
		if pw != f.password {
			reply(false, "")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "s1", Path: "/json"})
		reply(true, "")
		return
	}
	if c, err := r.Cookie("_session_id"); err != nil || c.Value != "s1" {
		reply(nil, "Not authenticated")
		return
	}
	switch {
	case req.Method == "web.connected":
		reply(f.connected, "")
	case req.Method == "web.get_hosts":
		reply(f.hosts, "")
	case req.Method == "web.get_host_status":
		var id string
		_ = json.Unmarshal(req.Params[0], &id)
		reply([]any{id, f.status[id], "2.2.0"}, "")
	case req.Method == "web.connect":
		var id string
		_ = json.Unmarshal(req.Params[0], &id)
		if f.status[id] != "Online" {
			reply(nil, "Host not online")
			return
		}
		f.connected = true
		reply([]string{"core.set_config"}, "")
	case strings.HasPrefix(req.Method, "core.") && !f.connected:
		reply(nil, "Unknown method")
	case req.Method == "core.set_config":
		var values map[string]float64
		_ = json.Unmarshal(req.Params[0], &values)
		for k, v := range values {
			f.config[k] = v
		}
		reply(nil, "")
	case req.Method == "core.get_config_value":
		var key string
		_ = json.Unmarshal(req.Params[0], &key)
		reply(f.config[key], "")
	default:
		reply(nil, "Unknown method")
	}
}

// state reads what the requests left behind.
func (f *fakeDeluge) state() (connected bool, down, up float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected, f.config["max_download_speed"], f.config["max_upload_speed"]
}

func (f *fakeDeluge) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.methods {
		if m == method {
			return true
		}
	}
	return false
}

// A fresh Web UI isn't connected to its daemon: connecting comes first.
func TestSetDelugeLimitsConnectsTheWebUIFirst(t *testing.T) {
	f, srv := newFakeDeluge(t)
	if err := SetDelugeLimits(context.Background(), srv.URL+"/", "deluge", 1500, 300); err != nil {
		t.Fatal(err)
	}
	connected, down, up := f.state()
	if !connected {
		t.Error("the Web UI was never connected to its daemon")
	}
	if down != 1500 || up != 300 {
		t.Errorf("limits = %v/%v, want 1500/300 KiB/s", down, up)
	}
}

// 0 is unlimited to AutoThrottle and a standstill to Deluge, which says -1.
func TestSetDelugeLimitsZeroIsUnlimited(t *testing.T) {
	f, srv := newFakeDeluge(t)
	f.config["max_download_speed"], f.config["max_upload_speed"] = 1500, 300
	if err := SetDelugeLimits(context.Background(), srv.URL, "deluge", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, down, up := f.state(); down != -1 || up != -1 {
		t.Errorf("limits = %v/%v, want -1/-1", down, up)
	}
}

func TestDelugeAlreadyConnectedSkipsTheHostList(t *testing.T) {
	f, srv := newFakeDeluge(t)
	f.connected = true
	if err := TestDeluge(context.Background(), srv.URL, "deluge"); err != nil {
		t.Fatal(err)
	}
	if f.called("web.get_hosts") || f.called("web.connect") {
		t.Error("reconnected a Web UI that was already connected")
	}
}

func TestDelugeConnectsToAnOnlineHost(t *testing.T) {
	f, srv := newFakeDeluge(t)
	f.hosts = [][]any{{"down", "10.0.0.9", 58846, ""}, {}, {"host-1", "127.0.0.1", 58846, "localclient"}}
	f.status["down"] = "Offline"
	if err := TestDeluge(context.Background(), srv.URL, "deluge"); err != nil {
		t.Fatal(err)
	}
	if connected, _, _ := f.state(); !connected {
		t.Error("never connected to the online host")
	}
}

func TestDelugeErrors(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(*fakeDeluge)
		password string
		url      func(string) string
		want     string
	}{
		{"wrong password", nil, "nope", nil, "password was rejected"},
		{"no daemon online", func(f *fakeDeluge) { f.status["host-1"] = "Offline" }, "deluge", nil, "no Deluge daemon"},
		{"no hosts at all", func(f *fakeDeluge) { f.hosts = nil }, "deluge", nil, "no Deluge daemon"},
		{"not a Web UI", nil, "deluge", func(u string) string { return u + "/elsewhere" }, "no Deluge Web UI"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeDeluge(t)
			if c.setup != nil {
				c.setup(f)
			}
			url := srv.URL
			if c.url != nil {
				url = c.url(url)
			}
			err := TestDeluge(context.Background(), url, c.password)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestDelugeHasNoUsername(t *testing.T) {
	if DownloaderUsesUsername("deluge") {
		t.Error("Deluge's Web UI has no username")
	}
	if !DownloaderUsesUsername("qbittorrent") || !DownloaderUsesUsername("transmission") {
		t.Error("qBittorrent and Transmission sign in with a username")
	}
	if RateLimiterFor("deluge") == nil {
		t.Error("deluge is not registered")
	}
}
