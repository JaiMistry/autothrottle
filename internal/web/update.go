package web

import (
	"context"
	"encoding/json"
	"fmt"
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
	State      string // idle | checking | current | available
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

// unreachable keeps the resting state's shape when the check itself
// could not run. There is no designed state for a failed check, and
// inventing one would say more about the failure than it deserves —
// but silently reporting "up to date" would be a lie.
func unreachable(why string) updateView {
	return updateView{State: "idle", Label: installedLabel(version.Version), NotePre: why}
}

const releasesAPI = "https://api.github.com/repos/JaiMistry/autothrottle/releases/latest"

// updateCheck asks GitHub for the latest release tag, on request only.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	view := checkLatestRelease(ctx, version.Version)
	if err := s.templates["configuration"].ExecuteTemplate(w, "updatecheck", view); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func checkLatestRelease(ctx context.Context, current string) updateView {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return unreachable("The update check could not be started.")
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return unreachable("No answer from GitHub. Expected on a box with no outbound internet access.")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return unreachable(fmt.Sprintf("GitHub answered %d.", resp.StatusCode))
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return unreachable("GitHub's answer could not be read.")
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	running := strings.TrimPrefix(current, "v")
	switch {
	case latest == "":
		return unreachable("GitHub reported no release tag.")
	case running == "dev":
		// A dev build has no version to compare against, and calling it
		// out of date would be wrong as often as not.
		return unreachable(fmt.Sprintf("This is a development build. v%s is the latest release.", latest))
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
