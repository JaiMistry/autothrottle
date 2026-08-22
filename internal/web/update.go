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
type updateView struct {
	State string // unknown | current | available | error
	Label string
	Note  string
}

// unknownUpdate is what About shows before anyone asks. Checking is
// deliberately manual: this app never contacts an update service on its
// own, and the copy says so rather than leaving the reader to wonder.
func unknownUpdate() updateView {
	return updateView{
		State: "unknown",
		Label: "Update status unknown",
		Note:  "Checks are manual — nothing phones home on its own.",
	}
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
		return updateView{State: "error", Label: "Could not check", Note: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return updateView{
			State: "error",
			Label: "Could not check",
			Note:  "No answer from GitHub. This is expected on a box with no outbound internet access.",
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		// Also what a private repository returns to an unauthenticated
		// caller, which is worth saying plainly rather than reporting as
		// a failure the user could act on.
		return updateView{
			State: "unknown",
			Label: "No published releases",
			Note:  "Nothing has been tagged yet, or the repository is not public.",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return updateView{
			State: "error",
			Label: "Could not check",
			Note:  fmt.Sprintf("GitHub answered %d.", resp.StatusCode),
		}
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return updateView{State: "error", Label: "Could not check", Note: "GitHub's answer could not be read."}
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	running := strings.TrimPrefix(current, "v")
	switch {
	case latest == "":
		return updateView{State: "unknown", Label: "No published releases", Note: "Nothing has been tagged yet."}
	case running == "dev":
		// A dev build has no release to compare against, and claiming it
		// is out of date would be wrong as often as not.
		return updateView{
			State: "unknown",
			Label: fmt.Sprintf("Latest release is v%s", latest),
			Note:  "This is a development build, so there is no version to compare it against.",
		}
	case latest == running:
		return updateView{State: "current", Label: "Up to date", Note: fmt.Sprintf("v%s is the latest release.", latest)}
	default:
		return updateView{
			State: "available",
			Label: fmt.Sprintf("v%s available", latest),
			Note:  fmt.Sprintf("You are on v%s. Pull the new image and restart.", running),
		}
	}
}
