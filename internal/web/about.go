package web

import (
	"fmt"
	"time"

	"github.com/JaiMistry/autothrottle/internal/version"
)

// aboutView is the read-only identity of the running process.
//
// Everything here is observed rather than configured: there is nothing
// on this tab to edit, which is why it reads as four flat rows instead
// of a form.
type aboutView struct {
	Commit    string
	BuiltNote string
	Runtime   string
	BootUnix  int64
	BootNote  string
	Auth      string
}

func (s *Server) aboutView() aboutView {
	b := version.Current()

	commit := b.Commit
	if commit == "" {
		commit = "unknown"
	}
	built := "Build date not recorded"
	if !b.Built.IsZero() {
		built = "Built: " + b.Built.Local().Format("2 Jan 2006")
	}

	rt := fmt.Sprintf("%s · %s/%s", b.Go, b.OS, b.Arch)
	if b.Docker {
		rt += " · docker"
	}

	return aboutView{
		Commit:    commit,
		BuiltNote: built,
		Runtime:   rt,
		BootUnix:  s.startedAt.UnixMilli(),
		BootNote:  "Last restarted: " + s.startedAt.Local().Format("2 Jan 2006, 15:04"),
		Auth:      s.authSummary(),
	}
}

// uptimeSince is the server-rendered first frame of the uptime counter.
// The page ticks it forward itself from the boot timestamp, so this only
// has to be right at the moment it is sent.
func uptimeSince(boot time.Time) string {
	d := time.Since(boot)
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd : %02dh : %02dm : %02ds",
		days, int(d.Hours())%24, int(d.Minutes())%60, int(d.Seconds())%60)
}
