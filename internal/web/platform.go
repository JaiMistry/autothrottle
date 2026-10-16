package web

import (
	"path/filepath"
	"runtime"
)

// Words that depend on how AutoThrottle was installed: from the Docker
// image, or with the Windows installer.

// host is where AutoThrottle runs, for advice about what it can reach:
// in Docker the container's networking is the usual suspect.
var host = hostFor(runtime.GOOS)

func hostFor(goos string) string {
	if goos == "windows" {
		return "this PC"
	}
	return "the AutoThrottle container"
}

// authNote says where the sign-in on About comes from.
func authNote(goos string) string {
	if goos == "windows" {
		return "Created on first run"
	}
	return "Set via environment variables at deploy time"
}

// logHint is where to look when something fails that only the log explains.
func logHint(goos, programData string) string {
	if goos == "windows" {
		return "Check the log in " + filepath.Join(programData, "AutoThrottle") + "."
	}
	return "Check the container log."
}

// installerURL is a release's Windows installer, as the release workflow
// names it.
func installerURL(version string) string {
	return "https://github.com/JaiMistry/autothrottle/releases/download/v" + version + "/AutoThrottle-Setup-" + version + ".exe"
}
