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

type dispatcharrVODClient struct {
	ID        string  `json:"client_id"`
	IP        string  `json:"client_ip"`
	UserAgent string  `json:"user_agent"`
	Position  float64 `json:"position_seconds"`
}

type dispatcharrVODContent struct {
	Type        string                 `json:"content_type"`
	ID          string                 `json:"content_uuid"`
	Name        string                 `json:"content_name"`
	ClientCount int                    `json:"connection_count"`
	Clients     []dispatcharrVODClient `json:"connections"`
	Metadata    struct {
		Duration float64 `json:"duration_secs"`
	} `json:"content_metadata"`
}

type dispatcharrCatchupSession struct {
	ID             string              `json:"session_id"`
	Name           string              `json:"channel_name"`
	ClientCount    int                 `json:"connection_count"`
	Clients        []dispatcharrClient `json:"connections"`
	Paused         bool                `json:"paused"`
	Position       *float64            `json:"playback_base_secs"`
	PositionAnchor *float64            `json:"position_anchor_at"`
}

// DispatcharrWatcher watches the sessions Dispatcharr exposes through its
// combined live, VOD and catch-up statistics. A reported connection counts
// even when paused. Active recording jobs use the live proxy and count too.
// Provider redirects and playback of finished DVR files do not count.
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
		} else if prev != session {
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

// Snapshot requires all three sections. Falling back to live-only statistics
// would silently release the throttle during VOD or catch-up playback.
func (w *DispatcharrWatcher) Snapshot(ctx context.Context) ([]Session, error) {
	var status struct {
		Live struct {
			Channels *[]dispatcharrChannel `json:"channels"`
		} `json:"live"`
		VOD struct {
			Contents *[]dispatcharrVODContent `json:"vod_connections"`
			Total    int                      `json:"total_connections"`
		} `json:"vod"`
		Catchup struct {
			Sessions *[]dispatcharrCatchupSession `json:"timeshift_sessions"`
			Total    int                          `json:"total_connections"`
		} `json:"catchup"`
	}
	if err := w.get(ctx, "/proxy/stats/", &status); err != nil {
		return nil, fmt.Errorf("reading Dispatcharr playback status: %w", err)
	}
	if status.Live.Channels == nil || status.VOD.Contents == nil || status.Catchup.Sessions == nil || status.VOD.Total < 0 || status.Catchup.Total < 0 {
		return nil, fmt.Errorf("Dispatcharr playback status must include live, VOD and catch-up lists")
	}
	sessions, err := w.liveSessions(ctx, *status.Live.Channels)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	vodReported := 0
	for _, content := range *status.VOD.Contents {
		if content.ID == "" || content.Type == "" || content.ClientCount < 0 {
			return nil, fmt.Errorf("invalid Dispatcharr VOD content identity or count")
		}
		seen := make(map[string]bool)
		for _, client := range content.Clients {
			if client.ID == "" || seen[client.ID] {
				return nil, fmt.Errorf("invalid Dispatcharr VOD client identity")
			}
			seen[client.ID] = true
			local := embySessionIsLocal(client.IP)
			if w.SkipLocalPlayback && local {
				continue
			}
			sessions = append(sessions, Session{
				Key:   "vod/" + content.Type + "/" + content.ID + "/" + client.ID,
				Title: content.Name, Device: client.UserAgent, Local: local,
				Position: dispatcharrSeconds(client.Position), PositionAt: now,
				Duration: dispatcharrSeconds(content.Metadata.Duration),
			})
		}
		count := max(content.ClientCount, len(seen))
		vodReported += count
		if content.ClientCount > len(seen) {
			sessions = append(sessions, Session{Key: "vod/" + content.Type + "/" + content.ID + "/unreported", Title: content.Name})
		}
	}
	if status.VOD.Total > vodReported {
		sessions = append(sessions, Session{Key: "vod/unreported", Title: "VOD"})
	}
	catchupReported := 0
	for _, playback := range *status.Catchup.Sessions {
		if playback.ID == "" || playback.ClientCount < 0 {
			return nil, fmt.Errorf("invalid Dispatcharr catch-up session identity or count")
		}
		var position time.Duration
		if playback.Position != nil {
			position = dispatcharrSeconds(*playback.Position)
			if !playback.Paused && playback.PositionAnchor != nil {
				position += dispatcharrSeconds(float64(now.UnixNano())/1e9 - *playback.PositionAnchor)
			}
		}
		seen := make(map[string]bool)
		for _, client := range playback.Clients {
			if client.ID == "" || seen[client.ID] {
				return nil, fmt.Errorf("invalid Dispatcharr catch-up client identity")
			}
			seen[client.ID] = true
			local := embySessionIsLocal(client.IP)
			if w.SkipLocalPlayback && local {
				continue
			}
			sessions = append(sessions, Session{
				Key:   "catchup/" + playback.ID + "/" + client.ID,
				Title: playback.Name, Device: client.UserAgent, Local: local,
				Paused: playback.Paused, Position: position, PositionAt: now,
			})
		}
		catchupReported += max(playback.ClientCount, len(seen))
		if playback.ClientCount > len(seen) {
			sessions = append(sessions, Session{Key: "catchup/" + playback.ID + "/unreported", Title: playback.Name, Paused: playback.Paused})
		}
	}
	if status.Catchup.Total > catchupReported {
		sessions = append(sessions, Session{Key: "catchup/unreported", Title: "Catch-up"})
	}
	return sessions, nil
}

func dispatcharrSeconds(seconds float64) time.Duration {
	// Treat absent, negative and overflowing timing metadata as unknown.
	if seconds <= 0 || seconds >= float64(1<<63-1)/float64(time.Second) {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// The live summary includes at most ten clients per channel. Request details
// when truncated so a remote viewer beyond those ten cannot be exempted.
func (w *DispatcharrWatcher) liveSessions(ctx context.Context, channels []dispatcharrChannel) ([]Session, error) {
	var sessions []Session
	for _, channel := range channels {
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
