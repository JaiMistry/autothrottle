package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// staticAssets serves the embedded assets with cache headers. Embedded
// files have no modification time, so each gets an ETag from its content
// hash instead, and a request carrying the matching ?v= is immutable.
type staticAssets struct {
	handler http.Handler
	hashes  map[string]string
}

func newStaticAssets(files fs.FS) *staticAssets {
	s := &staticAssets{
		handler: http.FileServer(http.FS(files)),
		hashes:  make(map[string]string),
	}
	_ = fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if sum, err := hashFile(files, path); err == nil {
			s.hashes["/"+path] = sum
		}
		return nil
	})
	return s
}

func hashFile(files fs.FS, path string) (string, error) {
	f, err := files.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// URL returns the path a template should reference, carrying the content
// hash so a new build busts the cache. The map is keyed by path inside the
// embedded tree, without the "/static" prefix.
func (s *staticAssets) URL(path string) string {
	if sum, ok := s.hashes[strings.TrimPrefix(path, "/static")]; ok {
		return path + "?v=" + sum
	}
	return path
}

func (s *staticAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clean := "/" + strings.TrimPrefix(r.URL.Path, "/")
	sum, known := s.hashes[clean]
	if known {
		w.Header().Set("ETag", `"`+sum+`"`)
		if r.URL.Query().Get("v") == sum {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// Unversioned (a bookmark, a typed path): revalidate.
			w.Header().Set("Cache-Control", "public, no-cache")
		}
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, sum) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	s.handler.ServeHTTP(w, r)
}
