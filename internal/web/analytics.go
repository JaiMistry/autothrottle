package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/JaiMistry/autothrottle/internal/analytics"
)

type downloaderOption struct {
	ID       int64
	Name     string
	Selected bool
}

type gapNote struct {
	Duration string
	Started  string
}

type analyticsData struct {
	pageData
	Ranges      []analytics.Range
	RangeKey    string
	Grain       string
	Downloaders []downloaderOption
	Selected    string // "all" or a downloader id
	HasClients  bool
	HasSamples  bool
	Interval    string
	RawDays     int
	RollupDays  int
	Stats       analytics.Stats
	Transferred string
	Throttled   string
	AvgPeriod   string
	Gaps        []gapNote
	ChartJSON   template.JS
}

func (s *Server) analyticsPage(w http.ResponseWriter, r *http.Request) {
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		slog.Error("listing downloaders", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rng := analytics.RangeFor(r.URL.Query().Get("range"))
	selected := r.URL.Query().Get("client")
	if selected == "" {
		selected = "all"
	}

	// Only enabled clients are sampled, so only they can be charted.
	var ids []int64
	options := make([]downloaderOption, 0, len(downloaders))
	for _, dl := range downloaders {
		options = append(options, downloaderOption{
			ID:       dl.ID,
			Name:     dl.Name,
			Selected: selected == strconv.FormatInt(dl.ID, 10),
		})
		if selected == "all" || selected == strconv.FormatInt(dl.ID, 10) {
			ids = append(ids, dl.ID)
		}
	}

	interval := s.settings.BandwidthSampleInterval()
	view, err := analytics.Build(s.db, ids, rng, interval, time.Now())
	if err != nil {
		slog.Error("building analytics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		slog.Error("encoding analytics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := analyticsData{
		pageData:    s.page(r, "Analytics", "analytics"),
		Ranges:      analytics.Ranges,
		RangeKey:    rng.Key,
		Grain:       rng.Grain,
		Downloaders: options,
		Selected:    selected,
		HasClients:  len(downloaders) > 0,
		HasSamples:  len(view.Down) > 0,
		Interval:    fmt.Sprintf("%ds", int(interval.Seconds())),
		RawDays:     int(s.settings.BandwidthRawRetention().Hours() / 24),
		RollupDays:  int(s.settings.BandwidthRollupRetention().Hours() / 24),
		Stats:       view.Stats,
		Transferred: byteLabel(view.Stats.TransferredBytes),
		Throttled:   durationLabel(time.Duration(view.Stats.ThrottledMs) * time.Millisecond),
		AvgPeriod:   durationLabel(time.Duration(view.Stats.AvgPeriodMs) * time.Millisecond),
		Gaps:        gapNotes(view.Gaps),
		ChartJSON:   template.JS(encoded),
	}
	s.render(w, "analytics", data)
}

// gapNotes describes each hole in the sample record. The reason is
// fixed rather than looked up: the sampler cannot record its own
// downtime, so the only thing that can be said honestly is that the app
// was not running.
func gapNotes(gaps []analytics.Span) []gapNote {
	out := make([]gapNote, 0, len(gaps))
	for _, g := range gaps {
		d := time.Duration(g.To-g.From) * time.Millisecond
		out = append(out, gapNote{
			Duration: durationLabel(d),
			Started:  time.UnixMilli(g.From).Local().Format("Jan 2, 3:04 PM"),
		})
	}
	return out
}

// setMapping turns one cell of the mappings matrix on or off. A cell
// has no id until it is first ticked, so this addresses a mapping by
// the pair it joins rather than by id.
func (s *Server) setMapping(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	serverID, err1 := strconv.ParseInt(r.FormValue("media_server_id"), 10, 64)
	downloaderID, err2 := strconv.ParseInt(r.FormValue("downloader_id"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "bad ids", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1"

	if err := s.db.SetMappingForPair(serverID, downloaderID, enabled); err != nil {
		slog.Error("setting mapping", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.applyConfigChange(false)

	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/configuration?tab=mappings", http.StatusSeeOther)
}
