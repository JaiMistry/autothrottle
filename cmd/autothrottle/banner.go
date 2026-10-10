package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/version"
)

// bannerArt is "AutoThrottle" in figlet's slant font, kept under 62
// columns so it does not wrap in a default-width terminal.
const bannerArt = `
    ___         __      ________               __  __  __
   /   | __  __/ /_____/_  __/ /_  _________  / /_/ /_/ /__
  / /| |/ / / / __/ __ \/ / / __ \/ ___/ __ \/ __/ __/ / _ \
 / ___ / /_/ / /_/ /_/ / / / / / / /  / /_/ / /_/ /_/ /  __/
/_/  |_\__,_/\__/\____/_/ /_/ /_/_/   \____/\__/\__/_/\___/
`

// startupInfo is what the banner reports beyond the build itself.
type startupInfo struct {
	DatabasePath string
	Address      string
	AuthMode     auth.Mode
	AuthUser     string
	MediaServers int
	Downloaders  int
	Mappings     int
}

// printBanner writes the human-readable block that opens the container
// log; everything after it is structured.
func printBanner(out io.Writer, build version.Build, info startupInfo) {
	var b strings.Builder
	b.WriteString(bannerArt)
	b.WriteString("  Throttles your download clients while something is playing.\n\n")

	runtimeDesc := fmt.Sprintf("%s %s/%s", build.Go, build.OS, build.Arch)
	if build.Docker {
		runtimeDesc += " (container)"
	}

	row := func(label, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "  %-11s %s\n", label, value)
	}
	row("Version", version.Label())
	row("Commit", build.Commit)
	row("Built", buildDate(build.Built))
	row("Runtime", runtimeDesc)
	row("Database", info.DatabasePath)
	row("Listening", info.Address)
	row("Auth", authDesc(info))
	row("Configured", configuredDesc(info))
	b.WriteString("\n")
	if info.AuthMode == auth.ModeSetup {
		b.WriteString("  First run: open the web UI to create your account.\n\n")
	}

	// One write per line: a multi-line write is one Docker log entry, and
	// viewers that prefix entries shift every line after the first.
	for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		_, _ = io.WriteString(out, line+"\n")
	}
}

// buildDate renders in the configured zone, like every other timestamp.
func buildDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2 Jan 2006 15:04 MST")
}

func authDesc(info startupInfo) string {
	switch info.AuthMode {
	case auth.ModeDisabled:
		return "disabled (anyone who can reach this port has full access)"
	case auth.ModeEnvironment:
		return "account " + info.AuthUser + " (from AUTH_USERNAME / AUTH_PASSWORD)"
	case auth.ModeAccount:
		return "account " + info.AuthUser
	default:
		return "not set up yet (open the web UI to create an account)"
	}
}

// configuredDesc reads "3 media servers · 2 downloaders · 5 mappings".
func configuredDesc(info startupInfo) string {
	if info.MediaServers == 0 && info.Downloaders == 0 {
		return "nothing yet (add a media server and a downloader to begin)"
	}
	parts := []string{
		count(info.MediaServers, "media server"),
		count(info.Downloaders, "downloader"),
		count(info.Mappings, "mapping"),
	}
	return strings.Join(parts, " · ")
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
