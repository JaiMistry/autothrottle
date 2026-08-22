package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

// templateFuncs are the helpers the templates need that html/template
// doesn't ship. Deliberately few: anything with real logic belongs in a
// handler where it can be tested, not in a template.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"dict":     dict,
		"cond":     cond,
		"mapkey":   func(a, b int64) string { return fmt.Sprintf("%d:%d", a, b) },
		"rate":     rateLabel,
		"bytes":    byteLabel,
		"duration": durationLabel,
		"pct":      pctLabel,
		"clock":    clockLabel,
		"stamp":    stampLabel,
		"lower":    strings.ToLower,
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
