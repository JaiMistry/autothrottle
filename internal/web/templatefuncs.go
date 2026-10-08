package web

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
)

// templateFuncs are the helpers the templates need that html/template
// doesn't ship. Deliberately few: anything with real logic belongs in a
// handler where it can be tested, not in a template.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"dict":            dict,
		"commas":          commas,
		"count":           count,
		"levelTone":       levelTone,
		"filterEmptyNote": filterEmptyNote,
		"mapkey":          mappingKey,
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
