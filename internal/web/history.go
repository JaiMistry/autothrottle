package web

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// historyDays is how many days the heatmap holds. Both ranges render
// all of them: the 7-day view hides the rest, so switching range is an
// animation in the page rather than a round trip.
const historyDays = 30

// historyView is the Dashboard's Throttle History card: minutes
// throttled per hour, a row per day, newest first.
type historyView struct {
	Range   string // "7d" or "30d"
	Days    int
	Total   time.Duration
	Empty   bool
	Corner  string // the newest row's month
	Rows    []historyRow
	Total7  int // minutes, for the range toggle's count
	Total30 int
	Empty7  bool
	Empty30 bool
}

type historyRow struct {
	Index      int
	Label      string // "Wed 7"
	Month      string // "Oct"
	MonthBreak bool   // the month changed since the row above
	Today      bool
	Cells      []historyCell
}

type historyCell struct {
	Future bool
	Hot    bool // throttled at least a minute: the only cells with a tooltip
	Step   int  // 0–4, the fill
	Side   string
	Value  string   // "42 min throttled so far"
	Spans  []string // "9:18 PM – 10:00 PM"
	When   string   // "Fri 2 Oct"
	Label  string   // the cell's aria-label
}

// throttleSteps are the heatmap's five fills, by minutes throttled in
// the hour. The legend draws the same five.
var throttleSteps = []int{0, 15, 30, 45, 60}

func stepOf(minutes int) int {
	for i, max := range throttleSteps {
		if minutes <= max {
			return i
		}
	}
	return len(throttleSteps) - 1
}

// buildHistory lays out throttle periods and process runs as the
// heatmap. A zero End on a period means it is still going; runs are
// the spans AutoThrottle was up, and an hour with less than a minute of
// them reads as not running rather than as nothing throttled.
func buildHistory(periods, runs []db.Span, now time.Time, rangeDays int) historyView {
	now = now.In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	v := historyView{Range: fmt.Sprintf("%dd", rangeDays), Days: rangeDays, Empty7: true, Empty30: true}
	var total7, total30 time.Duration

	for i := 0; i < historyDays; i++ {
		day := today.AddDate(0, 0, -i)
		row := historyRow{
			Index: i,
			Label: day.Format("Mon 2"),
			Month: day.Format("Jan"),
			Today: i == 0,
		}
		if i > 0 && row.Month != v.Rows[i-1].Month {
			row.MonthBreak = true
		}
		when := day.Format("Mon 2 Jan")
		for hr := 0; hr < 24; hr++ {
			a := time.Date(day.Year(), day.Month(), day.Day(), hr, 0, 0, 0, time.Local)
			b := time.Date(day.Year(), day.Month(), day.Day(), hr+1, 0, 0, 0, time.Local)
			if !a.Before(now) {
				row.Cells = append(row.Cells, historyCell{Future: true})
				continue
			}
			cell := historyCell{When: when}
			if hr < 6 {
				cell.Side = "l"
			} else if hr > 17 {
				cell.Side = "r"
			}
			if overlapAll(runs, a, b, now) < time.Minute {
				cell.Value = "AutoThrottle was off"
				cell.Label = cell.Value + " · " + when
				row.Cells = append(row.Cells, cell)
				continue
			}

			var throttled time.Duration
			for _, p := range periods {
				end, ongoing := p.End, p.End.IsZero()
				if ongoing {
					end = now
				}
				o := overlap(p.Start, end, a, b)
				if o <= 0 {
					continue
				}
				throttled += o
				from, to := later(p.Start, a), earlier(end, b)
				cell.Spans = append(cell.Spans, clockRange(from, to, ongoing && to.Equal(end)))
			}
			if i < 7 {
				total7 += throttled
			}
			total30 += throttled

			minutes := min(60, int(math.Round(throttled.Minutes())))
			soFar := ""
			if b.After(now) {
				soFar = " so far"
			}
			cell.Step = stepOf(minutes)
			if minutes > 0 {
				cell.Hot = true
				cell.Value = fmt.Sprintf("%d min throttled%s", minutes, soFar)
				cell.Label = strings.Join(append(append([]string{cell.Value}, cell.Spans...), when), " · ")
				if i < 7 {
					v.Empty7 = false
				}
				v.Empty30 = false
			} else {
				cell.Spans = nil
				cell.Value = "Not throttled" + soFar
				cell.Label = cell.Value + " · " + when
			}
			row.Cells = append(row.Cells, cell)
		}
		v.Rows = append(v.Rows, row)
	}

	v.Corner = v.Rows[0].Month
	v.Total7, v.Total30 = int(math.Round(total7.Minutes())), int(math.Round(total30.Minutes()))
	if rangeDays == 30 {
		v.Total, v.Empty = total30, v.Empty30
	} else {
		v.Total, v.Empty = total7, v.Empty7
	}
	return v
}

// overlapAll is how much of [a, b) the spans cover. A zero End runs to
// now.
func overlapAll(spans []db.Span, a, b, now time.Time) time.Duration {
	var d time.Duration
	for _, s := range spans {
		end := s.End
		if end.IsZero() {
			end = now
		}
		d += overlap(s.Start, end, a, b)
	}
	return d
}

func overlap(a0, a1, b0, b1 time.Time) time.Duration {
	return max(0, earlier(a1, b1).Sub(later(a0, b0)))
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// clockRange is "9:18 PM – 10:00 PM", or "9:18 PM – now" for a period
// still going.
func clockRange(from, to time.Time, ongoing bool) string {
	end := "now"
	if !ongoing {
		end = to.In(time.Local).Format("3:04 PM")
	}
	return from.In(time.Local).Format("3:04 PM") + " – " + end
}

// totalLabel is "18h 15m", "45m" or "3h".
func totalLabel(minutes int) string {
	switch {
	case minutes < 60:
		return fmt.Sprintf("%dm", minutes)
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	default:
		return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
	}
}

// loadHistory reads what the heatmap needs and lays it out.
func (s *Server) loadHistory(rangeDays int) (historyView, error) {
	now := time.Now()
	since := now.AddDate(0, 0, -historyDays-1)
	periods, err := s.db.ThrottlePeriods(since, now)
	if err != nil {
		return historyView{}, err
	}
	runs, err := s.db.Runs(since, now)
	if err != nil {
		return historyView{}, err
	}
	// This process is still running; its last heartbeat can be a minute
	// old, and the hour in progress must not read as partly down.
	if n := len(runs); n > 0 && now.Sub(runs[n-1].End) < 2*settings.HeartbeatInterval {
		runs[n-1].End = now
	}
	return buildHistory(periods, runs, now, rangeDays), nil
}

// historyRange reads ?range=, defaulting to the last 7 days.
func historyRange(r *http.Request) int {
	if r.URL.Query().Get("range") == "30d" {
		return 30
	}
	return 7
}

// historyCard serves the Throttle History card on its own, for its
// periodic refresh.
func (s *Server) historyCard(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadHistory(historyRange(r))
	if err != nil {
		slog.Error("building throttle history", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "dashboard", "throttlehistory", v)
}

// The caption's total, split into hours and minutes the way the page
// shows it: "18h 15m", "45m", "3h", "0m".
func (v historyView) totalMinutes() int { return int(math.Round(v.Total.Minutes())) }
func (v historyView) TotalH() int       { return v.totalMinutes() / 60 }
func (v historyView) TotalM() int       { return v.totalMinutes() % 60 }
func (v historyView) ShowH() bool       { return v.TotalH() > 0 }
func (v historyView) ShowM() bool       { return v.TotalM() > 0 || v.TotalH() == 0 }
func (v historyView) HDigits() int      { return len(fmt.Sprint(v.TotalH())) }
func (v historyView) MDigits() int      { return len(fmt.Sprint(v.TotalM())) }
func (v historyView) TotalText() string { return totalLabel(v.totalMinutes()) }
