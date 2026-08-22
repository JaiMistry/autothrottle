package web

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"
)

// templateFuncs are the helpers the templates need that html/template
// doesn't ship. Deliberately few: anything with real logic belongs in a
// handler where it can be tested, not in a template.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"dict":   dict,
		"cond":   cond,
		"commas": commas,
		"count":  count,
		// The analytics stats carry int64 counts.
		"count64":         func(n int64, one, many string) string { return count(int(n), one, many) },
		"levelTone":       levelTone,
		"gapBody":         gapBody,
		"filterEmptyNote": filterEmptyNote,
		"mapkey":          func(a, b int64) string { return fmt.Sprintf("%d:%d", a, b) },
		"rate":            rateLabel,
		"bytes":           byteLabel,
		"duration":        durationLabel,
		"pct":             pctLabel,
		"clock":           clockLabel,
		"stamp":           stampLabel,
		"lower":           strings.ToLower,
	}
}

// dict builds a map inline so one template can be called with named
// arguments instead of being split into a partial per call site.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict needs an even number of arguments, got %d", len(pairs))
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key %d is %T, want string", i, pairs[i])
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}

// cond picks between two values inline, for the many places a template
// needs one attribute value or another and an if/else would obscure the
// markup around it.
func cond(test bool, whenTrue, whenFalse any) any {
	if test {
		return whenTrue
	}
	return whenFalse
}

// commas groups an integer with thousands separators, matching the
// design's toLocaleString() on every rate figure.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// levelTone maps an event level onto the Badge tone that renders it.
func levelTone(level string) string {
	switch level {
	case "error":
		return "error"
	case "warn":
		return "warn"
	default:
		return "info"
	}
}

// filterEmptyNote says which filter came up empty, so the reader knows
// whether to clear the text or change the level.
func filterEmptyNote(level, search string) string {
	kind := ""
	if level != "" {
		kind = level + " "
	}
	if search != "" {
		return fmt.Sprintf("No %sevents match \u201c%s\u201d.", kind, search)
	}
	return fmt.Sprintf("No %sevents.", kind)
}

// gapBody writes the sentence under the chart naming every hole in the
// record. The reason is fixed: the sampler cannot log its own downtime,
// so the only honest thing to say is that the app was not running.
func gapBody(gaps []gapNote) string {
	parts := make([]string, 0, len(gaps))
	for _, g := range gaps {
		parts = append(parts, fmt.Sprintf("%s from %s", g.Duration, g.Started))
	}
	return strings.Join(parts, " · ") +
		" — AutoThrottle was not running. The chart breaks the line across these spans " +
		"rather than joining the ends, which would draw throughput that never happened."
}

// analyticsDuration matches the Analytics screen's own formatting:
// minutes alone under an hour, otherwise hours with zero-padded
// minutes, so a column of them stays aligned.
func analyticsDuration(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %02dm", m/60, m%60)
}

// splitBytes renders a byte total as a value and its unit separately,
// because a stat card sets the unit in smaller, dimmer type than the
// figure it qualifies.
func splitBytes(b int64) (string, string) {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f", float64(b)/(1<<40)), "TB"
	case b >= 1<<30:
		return fmt.Sprintf("%.1f", float64(b)/(1<<30)), "GB"
	default:
		return strconv.FormatInt(int64(float64(b)/(1<<20)+0.5), 10), "MB"
	}
}

// rateLabel renders a KB/s limit. 0 is unlimited and must never render
// as a bare "0", which reads as "stopped" rather than "no ceiling".
func rateLabel(kbps int64) string {
	if kbps <= 0 {
		return "unlimited"
	}
	if kbps >= 1024 {
		return fmt.Sprintf("%.1f MB/s", float64(kbps)/1024)
	}
	return fmt.Sprintf("%d KB/s", kbps)
}

// byteLabel renders a byte total at a sensible unit.
func byteLabel(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTP"[exp])
}

// durationLabel renders a span the way the design writes them: "4h 55m",
// "42m", "18s".
func durationLabel(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

func pctLabel(f float64) string { return fmt.Sprintf("%d%%", int(f*100+0.5)) }

// clockLabel is the wall time of an event, for a log row.
func clockLabel(t time.Time) string { return t.Local().Format("15:04:05") }

// stampLabel is a full local timestamp, for anything that may be older
// than today.
func stampLabel(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }
