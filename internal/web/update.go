package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/version"
)

// updateView is the About tab's release status.
//
// The note is split around its one emphasised word rather than carrying
// markup, so the version number can be bold without the view ever
// holding HTML.
type updateView struct {
	State      string // idle | checking | current | available | failed
	Label      string
	NotePre    string
	NoteStrong string
	NotePost   string
}

// idleUpdate is what About shows before anyone asks. Checking is
// deliberately manual: this app never contacts an update service on its
// own, so the resting state names the running build and nothing else.
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

// failedCheck is a check that did not produce a version to compare
// against. `note` is the line the card shows; the default covers a
// genuine transport failure, and callers override it when GitHub did
// answer, because "could not reach GitHub" would then be untrue and
// would send someone off debugging their network for nothing.
func failedCheck(why, note string) updateView {
	slog.Info("update check did not complete", "reason", why)
	if note == "" {
		note = "Could not reach GitHub. Try again later."
	}
	return updateView{State: "failed", Label: "Check failed", NotePre: note}
}

// notComparable is not a failure: the check worked, there is simply no
// release number on this build to compare against.
func notComparable(note string) updateView {
	return updateView{State: "idle", Label: installedLabel(version.Version), NotePre: note}
}

// minCheckDuration matches the connection test's floor in spirit: long
// enough for the spinner to register as a thing that happened.
const minCheckDuration = 700 * time.Millisecond

const releasesAPI = "https://api.github.com/repos/JaiMistry/autothrottle/releases/latest"

// updateCheck asks GitHub for the latest release tag, on request only.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	// The checking state is rendered client-side the moment the button is
	// pressed, so a reply that arrives in 40ms would replace it before it
	// could be read. Only a fast answer waits; a slow one is not delayed.
	started := time.Now()
	view := checkLatestRelease(ctx, version.Version)
	if elapsed := time.Since(started); elapsed < minCheckDuration {
		time.Sleep(minCheckDuration - elapsed)
	}
	if err := s.templates["configuration"].ExecuteTemplate(w, "updatecheck", view); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func checkLatestRelease(ctx context.Context, current string) updateView {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return failedCheck("request could not be built", "")
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return failedCheck("no answer from github", "")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		// Also what a private repository returns to an anonymous caller,
		// so the wording covers both without asserting which it is.
		return failedCheck("github answered 404",
			"GitHub has no published release for this repository yet.")
	}
	if resp.StatusCode != http.StatusOK {
		return failedCheck(fmt.Sprintf("github answered %d", resp.StatusCode),
			fmt.Sprintf("GitHub answered %d. Try again later.", resp.StatusCode))
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return failedCheck("github's answer could not be decoded", "GitHub's answer could not be read.")
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	running := strings.TrimPrefix(current, "v")
	switch {
	case latest == "":
		return failedCheck("github reported no release tag", "GitHub has no published release for this repository yet.")
	case running == "dev":
		// A dev build has no version to compare against, and calling it
		// out of date would be wrong as often as not.
		return notComparable(fmt.Sprintf("This is a development build. v%s is the latest release.", latest))
	case latest == running:
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
