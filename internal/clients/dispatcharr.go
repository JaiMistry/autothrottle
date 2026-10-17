package clients

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const dispatcharrPollInterval = 2 * time.Second

type dispatcharrClient struct {
	ID        string `json:"client_id"`
	IP        string `json:"ip_address"`
	UserAgent string `json:"user_agent"`
}

type dispatcharrChannel struct {
	ID          string              `json:"channel_id"`
	Name        string              `json:"channel_name"`
	ClientCount int                 `json:"client_count"`
	Clients     []dispatcharrClient `json:"clients"`
}

// DispatcharrWatcher watches connected live TS proxy clients. Dispatcharr
// has no player pause/position feed: a connected client counts until it
// disconnects, including a client consuming a recording.
type DispatcharrWatcher struct {
	BaseURL           string
	APIKey            string
	SkipLocalPlayback bool

	events       chan SessionEvent
	conn         connectionTracker
	pollInterval time.Duration
	active       map[string]Session
}

// NewDispatcharrWatcher takes an administrator API key, sent only in the
// X-API-Key header. No expiring login token or webhook is needed.
func NewDispatcharrWatcher(baseURL, apiKey string, skipLocalPlayback bool) *DispatcharrWatcher {
	return &DispatcharrWatcher{
		BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey,
		SkipLocalPlayback: skipLocalPlayback, events: make(chan SessionEvent, 16),
		pollInterval: dispatcharrPollInterval, active: make(map[string]Session),
	}
}

func (w *DispatcharrWatcher) Events() <-chan SessionEvent { return w.events }
func (w *DispatcharrWatcher) Connection() ConnectionState { return w.conn.Connection() }

func (w *DispatcharrWatcher) Run(ctx context.Context) {
	runWithBackoff(ctx, "dispatcharr", w.BaseURL, &w.conn, w.poll)
}

func (w *DispatcharrWatcher) poll(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		sessions, err := w.Snapshot(ctx)
		if err != nil {
			return err // Keep the last good state across outages.
		}
		if !w.conn.Connection().Connected {
			w.conn.connected()
		}
		w.update(ctx, sessions)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *DispatcharrWatcher) update(ctx context.Context, sessions []Session) {
	next := make(map[string]Session, len(sessions))
	for _, session := range sessions {
		next[session.Key] = session
	}
	previous := w.active
	w.active = next
	for key, session := range next {
		if prev, had := previous[key]; !had {
			emit(ctx, w.events, session, true)
		} else if prev.Title != session.Title || prev.Device != session.Device || prev.Local != session.Local {
			emitDetail(ctx, w.events, session)
		}
	}
	for key, session := range previous {
		if _, exists := next[key]; !exists {
			emit(ctx, w.events, session, false)
		}
	}
}

func (w *DispatcharrWatcher) get(ctx context.Context, path string, out any) error {
	return getJSON(ctx, w.BaseURL+path, func(r *http.Request) {
		r.Header.Set("X-API-Key", w.APIKey)
	}, out)
}

// Snapshot also serves the manager's reconciliation path. The summary
// includes at most ten clients per channel; request details when truncated
// so a remote viewer beyond those ten cannot be exempted as local.
func (w *DispatcharrWatcher) Snapshot(ctx context.Context) ([]Session, error) {
	var status struct {
		Channels *[]dispatcharrChannel `json:"channels"`
	}
	if err := w.get(ctx, "/proxy/ts/status", &status); err != nil {
		return nil, fmt.Errorf("reading Dispatcharr live proxy status: %w", err)
	}
	if status.Channels == nil {
		return nil, fmt.Errorf("Dispatcharr live proxy status is missing the channels list")
	}
	var sessions []Session
	for _, channel := range *status.Channels {
		if channel.ID == "" || channel.ClientCount < 0 {
			return nil, fmt.Errorf("invalid Dispatcharr channel status")
		}
		if channel.ClientCount == 0 {
			continue // A warmed buffer without clients is not playback.
		}
		if channel.ClientCount > len(channel.Clients) {
			id := channel.ID
			if err := w.get(ctx, "/proxy/ts/status/"+url.PathEscape(id), &channel); err != nil {
				return nil, fmt.Errorf("reading Dispatcharr channel clients: %w", err)
			}
			if channel.ID != id || channel.ClientCount < 0 {
				return nil, fmt.Errorf("invalid Dispatcharr channel detail")
			}
		}
		if channel.ClientCount == 0 {
			continue
		}
		seen := make(map[string]bool)
		for _, client := range channel.Clients {
			if client.ID == "" || seen[client.ID] {
				return nil, fmt.Errorf("invalid Dispatcharr client identity")
			}
			seen[client.ID] = true
			local := embySessionIsLocal(client.IP)
			if w.SkipLocalPlayback && local {
				continue
			}
			sessions = append(sessions, Session{
				Key: channel.ID + "/" + client.ID, Title: channel.Name,
				Device: client.UserAgent, Local: local,
			})
		}
		if channel.ClientCount > len(seen) {
			// A concurrent join or an older detail response may still omit
			// clients. Their locality is unknown: keep bandwidth reserved.
			sessions = append(sessions, Session{Key: channel.ID + "/unreported", Title: channel.Name})
		}
	}
	return sessions, nil
}

// TestDispatcharr validates the authenticated status response rather than
// the public frontend, which also returns 200 for unknown paths.
func TestDispatcharr(ctx context.Context, baseURL, apiKey string) error {
	_, err := NewDispatcharrWatcher(baseURL, apiKey, false).Snapshot(ctx)
	return err
}
