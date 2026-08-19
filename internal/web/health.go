package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
)

const healthCheckTimeout = 8 * time.Second

type backendStatus struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Reachable bool   `json:"reachable"`
	// Watched reports whether a live session watcher is running for
	// this media server. Reachable but unwatched means throttling
	// won't trigger from it, which a bare reachability check hides.
	Watched bool   `json:"watched,omitempty"`
	Error   string `json:"error,omitempty"`
}

type healthResponse struct {
	Status       string          `json:"status"`
	MediaServers []backendStatus `json:"media_servers"`
	Downloaders  []backendStatus `json:"downloaders"`
}

// healthHandler reports real per-backend reachability, not just "the
// internal loop is alive" — per plan.md's Security section, this was
// one of the original app's known bugs.
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()

	resp := healthResponse{Status: "ok"}

	if mediaServers, err := s.db.ListMediaServers(); err == nil {
		for _, ms := range mediaServers {
			if !ms.Enabled {
				continue
			}
			bs := backendStatus{Name: ms.Name, Type: ms.Type}
			if s.watchers != nil {
				bs.Watched = s.watchers.Running(ms.ID)
			}
			if token, err := crypto.Decrypt(s.encryptionKey, ms.CredentialEncrypted); err != nil {
				bs.Error = "credential could not be decrypted"
			} else if err := clients.TestPlex(ctx, ms.URL, token); err != nil {
				bs.Error = err.Error()
			} else {
				bs.Reachable = true
			}
			if !bs.Reachable || !bs.Watched {
				resp.Status = "degraded"
			}
			resp.MediaServers = append(resp.MediaServers, bs)
		}
	}

	if downloaders, err := s.db.ListDownloaders(); err == nil {
		for _, dl := range downloaders {
			if !dl.Enabled {
				continue
			}
			bs := backendStatus{Name: dl.Name, Type: dl.Type}
			username, password, credErr := s.decodeDownloaderCredential(dl)
			if credErr != nil {
				bs.Error = credErr.Error()
			} else if err := clients.TestQBittorrent(ctx, dl.URL, username, password); err != nil {
				bs.Error = err.Error()
			} else {
				bs.Reachable = true
			}
			if !bs.Reachable {
				resp.Status = "degraded"
			}
			resp.Downloaders = append(resp.Downloaders, bs)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if resp.Status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// decodeDownloaderCredential returns the decrypted username/password
// for a downloader with a stored credential, or empty strings for one
// with none (a legitimate no-auth configuration, not an error).
func (s *Server) decodeDownloaderCredential(dl db.Downloader) (string, string, error) {
	if !dl.CredentialEncrypted.Valid || dl.CredentialEncrypted.String == "" {
		return "", "", nil
	}
	decrypted, err := crypto.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		return "", "", err
	}
	cred, err := credential.DecodeDownloader(decrypted)
	if err != nil {
		return "", "", err
	}
	return cred.Username, cred.Password, nil
}
