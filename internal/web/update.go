package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/version"
)

// updateView is the About tab's release status. The note is split around
// its one bold word, so the view never holds HTML.
type updateView struct {
	State      string // idle | checking | current | available | failed
	Label      string
	NotePre    string
	NoteStrong string
	NotePost   string
}

// idleUpdate is what About shows before anyone asks: checks are manual
// only, never automatic.
func idleUpdate() updateView {
	return updateView{
		State:   "idle",
		Label:   installedLabel(version.Version),
		NotePre: "Not checked for updates yet.",
	}
}

// installedLabel reads "v0.4.2 installed", or says "dev build" when
// there is no release number to print.
func installedLabel(v string) string {
	if v == "" || v == "dev" {
		return "dev build installed"
	}
	return fmt.Sprintf("v%s installed", strings.TrimPrefix(v, "v"))
}

// failedCheck is a check that produced no version to compare. The card
// always says the same thing; the cause goes to the log.
func failedCheck(why string) updateView {
	slog.Info("update check did not complete", "reason", why)
	return updateView{
		State: "failed", Label: "Check failed",
		NotePre: "Could not reach GitHub. Try again later.",
	}
}

// notComparable is not a failure: the check worked, there is simply no
// release number on this build to compare against.
func notComparable(note string) updateView {
	return updateView{State: "idle", Label: installedLabel(version.Version), NotePre: note}
}

// minCheckDuration keeps a fast reply from replacing the checking state
// before it can be read.
const minCheckDuration = 700 * time.Millisecond

var releasesAPI = "https://api.github.com/repos/JaiMistry/autothrottle/releases/latest"

// updateCheck asks GitHub for the latest release tag, on request only.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	started := time.Now()
	view := checkLatestRelease(ctx, version.Number())
	if elapsed := time.Since(started); elapsed < minCheckDuration {
		time.Sleep(minCheckDuration - elapsed)
	}
	s.renderFragment(w, "configuration", "updatecheck", view)
}

// checkLatestRelease compares a release number with the newest published
// tag. current is empty for a build with no release number.
func checkLatestRelease(ctx context.Context, current string) updateView {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return failedCheck("request could not be built")
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return failedCheck("no answer from github")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		// No release yet, or a private repository.
		return failedCheck("github answered 404")
	}
	if resp.StatusCode != http.StatusOK {
		return failedCheck(fmt.Sprintf("github answered %d", resp.StatusCode))
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return failedCheck("github's answer could not be decoded")
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	running := current
	switch {
	case latest == "":
		return failedCheck("github reported no release tag")
	case running == "" || running == "dev":
		return notComparable(fmt.Sprintf("This is a development build. v%s is the latest release.", latest))
	case compareVersions(running, latest) >= 0:
		// Equal, or ahead (an unreleased build): nothing to fetch.
		return updateView{
			State: "current", Label: "Up to date",
			NoteStrong: "v" + latest, NotePost: " is the latest release.",
		}
	default:
		return updateView{
			State: "available", Label: fmt.Sprintf("v%s available", latest),
			NotePre: "You are on ", NoteStrong: "v" + running, NotePost: ". Pull the new image and restart.",
		}
	}
}

// compareVersions orders two dot-separated release numbers, returning -1,
// 0 or 1. Segments compare numerically (1.0.10 > 1.0.9), a pre-release
// sorts below its release, and anything unparseable compares equal, so an
// update is never announced that may not exist.
func compareVersions(a, b string) int {
	aNum, aPre := splitPreRelease(a)
	bNum, bPre := splitPreRelease(b)

	aParts, bParts := strings.Split(aNum, "."), strings.Split(bNum, ".")
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		if c := compareSegment(segment(aParts, i), segment(bParts, i)); c != 0 {
			return c
		}
	}
	switch {
	case aPre == bPre:
		return 0
	case aPre == "": // a release outranks any pre-release of itself
		return 1
	case bPre == "":
		return -1
	case aPre < bPre:
		return -1
	default:
		return 1
	}
}

func splitPreRelease(v string) (string, string) {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func segment(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return "0" // 1.0 and 1.0.0 are the same release
}

func compareSegment(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	if aErr != nil || bErr != nil {
		return 0 // unparseable: treat as equal rather than guess
	}
	switch {
	case an < bn:
		return -1
	case an > bn:
		return 1
	default:
		return 0
	}
}
