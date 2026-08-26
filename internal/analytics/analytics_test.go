package analytics

import (
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// fakeStore serves a fixed set of bandwidth points and nothing else,
// which is all the windowing behaviour depends on.
type fakeStore struct {
	points   []db.BandwidthPoint
	sessions []db.SessionRecord
}

func (f fakeStore) BandwidthSeries(_ int64, since, until time.Time, _ time.Duration) ([]db.BandwidthPoint, error) {
	var out []db.BandwidthPoint
	for _, p := range f.points {
		if !p.At.Before(since) && !p.At.After(until) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (fakeStore) ThrottlePeriods(int64, time.Time, time.Time, time.Duration) ([]db.ThrottlePeriod, error) {
	return nil, nil
}
func (fakeStore) BandwidthSummary(int64, time.Time, time.Time, time.Duration) (db.BandwidthSummary, error) {
	return db.BandwidthSummary{}, nil
}
func (fakeStore) ObservedSampleInterval(int64, time.Time, time.Time) (time.Duration, bool) {
	return 0, false
}
func (f fakeStore) ListSessions(int64, time.Time, time.Time) ([]db.SessionRecord, error) {
	return f.sessions, nil
}
func (fakeStore) SessionSummary(int64, time.Time, time.Time) (int64, int64, error) { return 0, 0, nil }
func (fakeStore) ListMediaServers() ([]db.MediaServer, error) {
	return []db.MediaServer{{ID: 1, Name: "Living Room"}}, nil
}

func rangeByKey(t *testing.T, key string) Range {
	t.Helper()
	for _, r := range Ranges {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no range %q", key)
	return Range{}
}

// An install with three hours of history asked for 30 days must not
// draw 30 days of axis with everything crushed against the right edge.
func TestBuildFitsAxisToAvailableHistory(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	rng := rangeByKey(t, "30d")

	var points []db.BandwidthPoint
	for at := now.Add(-3 * time.Hour); !at.After(now); at = at.Add(rng.Bucket) {
		points = append(points, db.BandwidthPoint{At: at, DownloadBytesPerSec: 1000, SampleCount: 1})
	}

	view, err := Build(fakeStore{points: points}, []int64{1}, rng, time.Minute, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	span := time.Duration(view.To-view.From) * time.Millisecond
	if span > 6*time.Hour {
		t.Errorf("axis spans %v, want it fitted to the ~3h of history", span)
	}
	if got := time.UnixMilli(view.From).UTC(); got.Before(now.Add(-6 * time.Hour)) {
		t.Errorf("axis starts at %v, well before the first sample", got)
	}
}

// With a full window of history the axis is the window: fitting must
// not shrink a chart that has the data to fill itself.
func TestBuildKeepsFullWindowWhenHistoryCoversIt(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	rng := rangeByKey(t, "24h")

	var points []db.BandwidthPoint
	for at := now.Add(-rng.Span); !at.After(now); at = at.Add(rng.Bucket) {
		points = append(points, db.BandwidthPoint{At: at, DownloadBytesPerSec: 1000, SampleCount: 1})
	}

	view, err := Build(fakeStore{points: points}, []int64{1}, rng, time.Minute, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	span := time.Duration(view.To-view.From) * time.Millisecond
	if span < rng.Span-rng.Bucket {
		t.Errorf("axis spans %v, want the full %v", span, rng.Span)
	}
}

// Nothing recorded at all keeps the nominal window, so an empty chart
// still shows the period it is empty for.
func TestBuildKeepsWindowWhenThereIsNoData(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	rng := rangeByKey(t, "7d")

	view, err := Build(fakeStore{}, []int64{1}, rng, time.Minute, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	span := time.Duration(view.To-view.From) * time.Millisecond
	if span < rng.Span-rng.Bucket {
		t.Errorf("empty view spans %v, want the full %v", span, rng.Span)
	}
}

func TestFirstDatum(t *testing.T) {
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC).UnixMilli()

	if _, ok := firstDatum(nil, nil); ok {
		t.Error("firstDatum on nothing reported a datum")
	}

	got, ok := firstDatum([]int64{base + 5000, base, base + 1000}, nil)
	if !ok || got.UnixMilli() != base {
		t.Errorf("firstDatum = %v (%v), want the earliest bucket", got, ok)
	}

	// A session recorded before the first bandwidth sample still counts.
	got, ok = firstDatum([]int64{base}, []Session{{Start: base - 60000}})
	if !ok || got.UnixMilli() != base-60000 {
		t.Errorf("firstDatum = %v (%v), want the earlier session start", got, ok)
	}
}
