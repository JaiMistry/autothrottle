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
	Subject       string
	Subtitle      string
	MultiClient   bool
	Hourly        bool
	TransferValue string
	TransferUnit  string
	PeriodsNote   string
	SessionsNote  string
	LimitLegend   string
	HasLimit      bool
	HasBands      bool
	SessionRows   int
	SessionHeight int
	Ranges        []analytics.Range
	RangeKey      string
	Grain         string
	Downloaders   []downloaderOption
	Selected      string // "all" or a downloader id
	HasClients    bool
	HasSamples    bool
	Interval      string
	RawDays       int
	RollupDays    int
	Stats         analytics.Stats
	Throttled     string
	Gaps          []gapNote
	ChartJSON     template.JS
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

	// The interval is user-configurable, so it is rendered rather than
	// written into copy.
	intervalLabel := fmt.Sprintf("%ds", int(interval.Seconds()))
	if interval >= 2*time.Minute {
		intervalLabel = fmt.Sprintf("%d minutes", int(interval.Minutes()))
	}

	subject := "every download client's"
	multi := len(downloaders) > 1 && selected == "all"
	if !multi {
		for _, dl := range downloaders {
			if selected == "all" || selected == strconv.FormatInt(dl.ID, 10) {
				subject = dl.Name + "'s"
				break
			}
		}
	}

	rawDays := int(s.settings.BandwidthRawRetention().Hours() / 24)
	rollupDays := int(s.settings.BandwidthRollupRetention().Hours() / 24)

	transferValue, transferUnit := splitBytes(view.Stats.TransferredBytes)
	periodsNote := "none in this window"
	if view.Stats.Periods > 0 {
		periodsNote = "avg " + analyticsDuration(time.Duration(view.Stats.AvgPeriodMs)*time.Millisecond)
	}
	limitLegend := "Limit in effect"
	hasLimit := !rng.Hourly
	if multi {
		limitLegend = "Combined limit in effect"
	}
	if !hasLimit {
		limitLegend = "Limit in effect — not retained at hourly resolution"
	}
	servers := map[string]bool{}
	for _, sess := range view.Sess {
		servers[sess.Server] = true
	}

	data := analyticsData{
		pageData:    s.page(r, "Analytics", "analytics"),
		Subject:     subject,
		MultiClient: multi,
		Hourly:      rng.Hourly,
		Subtitle: fmt.Sprintf(
			"Polled from %s own rate counters every %s. Full-resolution samples for %d days, hourly averages for %d days.",
			subject, intervalLabel, rawDays, rollupDays),
		TransferValue: transferValue,
		TransferUnit:  transferUnit,
		PeriodsNote:   periodsNote,
		SessionsNote: fmt.Sprintf("across %s",
			count(int(view.Stats.Servers), "server", "servers")),
		LimitLegend: limitLegend,
		HasLimit:    hasLimit,
		HasBands:    len(view.Bands) > 0,
		SessionRows: len(servers),
		// The timeline grows a row per server rather than scrolling: a
		// reader comparing it to the chart above needs both fully
		// visible at once.
		SessionHeight: maxInt(96, len(servers)*46+40),
		Ranges:        analytics.Ranges,
		RangeKey:      rng.Key,
		Grain:         rng.Grain,
		Downloaders:   options,
		Selected:      selected,
		HasClients:    len(downloaders) > 0,
		HasSamples:    len(view.Down) > 0,
		Interval:      intervalLabel,
		RawDays:       rawDays,
		RollupDays:    rollupDays,
		Stats:         view.Stats,
		Throttled:     analyticsDuration(time.Duration(view.Stats.ThrottledMs) * time.Millisecond),
		Gaps:          gapNotes(view.Gaps),
		ChartJSON:     template.JS(encoded),
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
			Duration: analyticsDuration(d),
			Started:  time.UnixMilli(g.From).Local().Format("Jan 2, 15:04"),
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

	// Re-render the matrix rather than answering 204. HTMX does nothing
	// with an empty response, so the cell kept its old label and the
	// toggle read as broken even though the mapping had changed.
	if r.Header.Get("HX-Request") == "true" {
		data, err := s.buildConfigPageData(r, "mappings")
		if err != nil {
			slog.Error("rebuilding mappings after toggle", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := s.templates["configuration"].ExecuteTemplate(w, "mappingmatrix", data); err != nil {
			slog.Error("rendering mappings matrix", "error", err)
		}
		return
	}
	http.Redirect(w, r, "/configuration?tab=mappings", http.StatusSeeOther)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
