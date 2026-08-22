// Package analytics composes the stored bandwidth samples, throttle
// periods and session history into the view the Analytics page draws.
//
// It lives outside internal/db because the shaping here is presentation
// logic, not storage: which ranges exist, how coarse each one is, what
// happens when several download clients are combined onto one axis.
package analytics

import (
	"fmt"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// Range is one of the page's three windows. The range selector changes
// the chart's *character*, not just its width: past the
// full-resolution retention window only hourly averages survive, and
// the applied-limit series was never retained that far back at all.
type Range struct {
	Key    string
	Label  string
	Span   time.Duration
	Bucket time.Duration
	// Grain is stated on the chart itself, so smoothed data is never
	// read as real detail.
	Grain string
	// Hourly marks the range that runs on rollups. The limit series and
	// the throttle bands are dropped there — the limit because it was
	// never stored, the bands because a month of nightly viewing draws
	// thirty slivers that read as noise rather than as information.
	Hourly bool
}

var Ranges = []Range{
	{Key: "24h", Label: "24h", Span: 24 * time.Hour, Bucket: 5 * time.Minute, Grain: "5-minute samples"},
	{Key: "7d", Label: "7d", Span: 7 * 24 * time.Hour, Bucket: 30 * time.Minute, Grain: "30-minute samples"},
	{Key: "30d", Label: "30d", Span: 30 * 24 * time.Hour, Bucket: time.Hour, Grain: "hourly averages", Hourly: true},
}

// RangeFor resolves a range key, falling back to 24h.
func RangeFor(key string) Range {
	for _, r := range Ranges {
		if r.Key == key {
			return r
		}
	}
	return Ranges[0]
}

// Point is one sample on the throughput chart. Nil means no data: the
// line must break rather than joining across it.
type Point struct {
	At    int64    `json:"x"`
	Value *float64 `json:"y"`
}

// Span is a period on the time axis — a throttle band, a gap, or a
// session.
type Span struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Session is one playback span, attributed to its server.
type Session struct {
	Server  string `json:"server"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Ongoing bool   `json:"ongoing"`
}

// Stats are the four cards above the charts.
type Stats struct {
	TransferredBytes int64   `json:"transferredBytes"`
	ThrottledMs      int64   `json:"throttledMs"`
	ObservedMs       int64   `json:"observedMs"`
	ThrottledPct     int     `json:"throttledPct"`
	Periods          int     `json:"periods"`
	AvgPeriodMs      int64   `json:"avgPeriodMs"`
	Sessions         int64   `json:"sessions"`
	Servers          int64   `json:"servers"`
	throttledShare   float64 // retained for rounding decisions
}

// View is everything the page needs, ready to serialise.
type View struct {
	Range  string    `json:"range"`
	Grain  string    `json:"grain"`
	From   int64     `json:"from"`
	To     int64     `json:"to"`
	Bucket int64     `json:"bucket"`
	Down   []Point   `json:"down"`
	Up     []Point   `json:"up"`
	Limit  []Point   `json:"limit"`
	Bands  []Span    `json:"bands"`
	Gaps   []Span    `json:"gaps"`
	Sess   []Session `json:"sessions"`
	Stats  Stats     `json:"stats"`
}

// Store is the slice of the database this package reads.
type Store interface {
	BandwidthSeries(downloaderID int64, since, until time.Time, bucket time.Duration) ([]db.BandwidthPoint, error)
	ThrottlePeriods(downloaderID int64, since, until time.Time, sampleInterval time.Duration) ([]db.ThrottlePeriod, error)
	BandwidthSummary(downloaderID int64, since, until time.Time, sampleInterval time.Duration) (db.BandwidthSummary, error)
	ListSessions(downloaderID int64, since, until time.Time) ([]db.SessionRecord, error)
	SessionSummary(downloaderID int64, since, until time.Time) (int64, int64, error)
	ListMediaServers() ([]db.MediaServer, error)
}

// Build assembles the view for one downloader, or for every downloader
// when ids holds more than one.
//
// Combining clients is not a sum of everything: rates add, because the
// total is what the link actually carried; throttle time is the
// *union*, because two clients throttled in the same minute is one
// throttled minute; and a bucket where any client has no sample is a
// gap for the total, since the sum of what was measured and what was
// not is not a measurement.
func Build(store Store, ids []int64, rng Range, sampleInterval time.Duration, now time.Time) (View, error) {
	if len(ids) == 0 {
		return View{Range: rng.Key, Grain: rng.Grain,
			From: ms(now.Add(-rng.Span)), To: ms(now), Bucket: rng.Bucket.Milliseconds()}, nil
	}

	since, until := now.Add(-rng.Span), now
	view := View{
		Range:  rng.Key,
		Grain:  rng.Grain,
		From:   ms(since),
		To:     ms(until),
		Bucket: rng.Bucket.Milliseconds(),
	}

	// Buckets are keyed on absolute time, so every client's series lands
	// on the same boundaries and can be summed position by position.
	type agg struct {
		down, up  float64
		limitKBps int64
		limited   bool
		reporting int
	}
	buckets := map[int64]*agg{}
	var order []int64

	for _, id := range ids {
		points, err := store.BandwidthSeries(id, since, until, rng.Bucket)
		if err != nil {
			return View{}, err
		}
		for _, p := range points {
			key := p.At.UnixMilli()
			b, ok := buckets[key]
			if !ok {
				b = &agg{limited: true}
				buckets[key] = b
				order = append(order, key)
			}
			b.down += float64(p.DownloadBytesPerSec)
			b.up += float64(p.UploadBytesPerSec)
			b.reporting++
			// One unlimited client makes the combined ceiling unlimited,
			// so the total limit only draws when every client has one.
			if p.DownloadLimitKBps > 0 {
				b.limitKBps += p.DownloadLimitKBps
			} else {
				b.limited = false
			}
		}
	}

	sortInt64(order)

	for _, key := range order {
		b := buckets[key]
		// A bucket only counts as measured when every selected client
		// reported in it.
		complete := b.reporting == len(ids)
		if !complete {
			view.Down = append(view.Down, Point{At: key})
			view.Up = append(view.Up, Point{At: key})
			view.Limit = append(view.Limit, Point{At: key})
			continue
		}
		down, up := b.down, b.up
		view.Down = append(view.Down, Point{At: key, Value: &down})
		view.Up = append(view.Up, Point{At: key, Value: &up})

		// The limit series is not retained past the full-resolution
		// window, and a limit of 0 means no ceiling — both are a null
		// here, which the tooltip reads as "Unlimited" only when the
		// rates beside it are present.
		if rng.Hourly || !b.limited || b.limitKBps == 0 {
			view.Limit = append(view.Limit, Point{At: key})
		} else {
			v := float64(b.limitKBps * 1024)
			view.Limit = append(view.Limit, Point{At: key, Value: &v})
		}

	}

	// Totals come from each client's own summary and are added, rather
	// than being re-derived from the combined series: the summary
	// already weights every bucket by how many observations it holds,
	// which a bucketed average has thrown away.
	var transferred int64
	for _, id := range ids {
		summary, err := store.BandwidthSummary(id, since, until, sampleInterval)
		if err != nil {
			return View{}, err
		}
		transferred += summary.DownloadBytes + summary.UploadBytes
	}

	view.Gaps = gapsFrom(order, ms(since), ms(until), rng.Bucket)

	// Throttle spans: union across clients, so overlapping throttling
	// counts once.
	var spans []Span
	for _, id := range ids {
		periods, err := store.ThrottlePeriods(id, since, until, sampleInterval)
		if err != nil {
			return View{}, err
		}
		for _, p := range periods {
			spans = append(spans, Span{From: ms(p.Start), To: ms(p.End)})
		}
	}
	union := mergeSpans(spans)
	if !rng.Hourly {
		view.Bands = union
	}

	sessions, err := buildSessions(store, ids, since, until, now)
	if err != nil {
		return View{}, err
	}
	view.Sess = sessions

	var sessionCount, serverCount int64
	if len(ids) == 1 {
		sessionCount, serverCount, err = store.SessionSummary(ids[0], since, until)
		if err != nil {
			return View{}, err
		}
	} else {
		seen := map[string]bool{}
		for _, s := range sessions {
			seen[s.Server] = true
		}
		sessionCount, serverCount = int64(len(sessions)), int64(len(seen))
	}

	// Percentages are shares of time actually observed: an outage comes
	// out of the denominator rather than counting as time spent not
	// throttling.
	observed := rng.Span - gapDuration(view.Gaps)
	var throttled time.Duration
	for _, s := range union {
		throttled += time.Duration(s.To-s.From) * time.Millisecond
	}

	view.Stats = Stats{
		TransferredBytes: transferred,
		ThrottledMs:      throttled.Milliseconds(),
		ObservedMs:       observed.Milliseconds(),
		Periods:          len(union),
		Sessions:         sessionCount,
		Servers:          serverCount,
	}
	if observed > 0 {
		view.Stats.throttledShare = float64(throttled) / float64(observed)
		view.Stats.ThrottledPct = int(view.Stats.throttledShare*100 + 0.5)
	}
	if len(union) > 0 {
		view.Stats.AvgPeriodMs = throttled.Milliseconds() / int64(len(union))
	}
	return view, nil
}

func buildSessions(store Store, ids []int64, since, until, now time.Time) ([]Session, error) {
	servers, err := store.ListMediaServers()
	if err != nil {
		return nil, err
	}
	name := make(map[int64]string, len(servers))
	for _, ms := range servers {
		name[ms.ID] = ms.Name
	}

	seen := map[string]bool{}
	var out []Session
	for _, id := range ids {
		records, err := store.ListSessions(id, since, until)
		if err != nil {
			return nil, err
		}
		for _, rec := range records {
			start, err := time.Parse(db.TimeFormat, rec.StartedAt)
			if err != nil {
				continue
			}
			end, ongoing := now, true
			if rec.Ended() {
				parsed, err := time.Parse(db.TimeFormat, rec.EndedAt.String)
				if err == nil {
					end, ongoing = parsed, false
				}
			}
			// A session mapped to two selected clients is still one
			// session on the timeline.
			key := fmt.Sprintf("%d|%s|%s", rec.MediaServerID, rec.StartedAt, rec.SessionKey)
			if seen[key] {
				continue
			}
			seen[key] = true
			if start.Before(since) {
				start = since
			}
			out = append(out, Session{
				Server:  name[rec.MediaServerID],
				Start:   ms(start),
				End:     ms(end),
				Ongoing: ongoing,
			})
		}
	}
	sortSessions(out)
	return out, nil
}

// gapsFrom turns the buckets that exist into the spans that don't. A
// gap means the app was not running; the chart draws it hatched and an
// alert underneath names its duration.
func gapsFrom(present []int64, from, to int64, bucket time.Duration) []Span {
	step := bucket.Milliseconds()
	if step <= 0 {
		return nil
	}
	var gaps []Span
	cursor := from
	for _, at := range present {
		if at-cursor >= step*2 {
			gaps = append(gaps, Span{From: cursor, To: at})
		}
		if at+step > cursor {
			cursor = at + step
		}
	}
	if to-cursor >= step*2 {
		gaps = append(gaps, Span{From: cursor, To: to})
	}
	return gaps
}

func gapDuration(gaps []Span) time.Duration {
	var total time.Duration
	for _, g := range gaps {
		total += time.Duration(g.To-g.From) * time.Millisecond
	}
	return total
}

// mergeSpans unions overlapping spans, so two clients throttled at the
// same moment is one throttled moment.
func mergeSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sortSpans(spans)
	out := []Span{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s.From <= last.To {
			if s.To > last.To {
				last.To = s.To
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func sortInt64(v []int64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func sortSpans(v []Span) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j].From < v[j-1].From; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func sortSessions(v []Session) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j].Start < v[j-1].Start; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
