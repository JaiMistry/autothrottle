package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// staticAssets serves the embedded CSS, JS, fonts and images with
// cache headers Go's plain FileServer cannot provide here.
//
// Embedded files carry a zero modification time, so http.FileServer
// emits no Last-Modified and can answer no conditional request — which
// means the browser re-downloads every asset on every navigation. On a
// server-rendered app that is every click, and the 100KB stylesheet
// being refetched each time is what makes navigation feel heavy and
// can leave a moment of unstyled HTML on screen.
//
// Each file gets an ETag from its content hash. A request carrying the
// matching ?v= is answered as immutable, because the hash *is* the
// version: a new build changes the hash, which changes the URL, so a
// stale asset can never be served under a new one.
type staticAssets struct {
	files   fs.FS
	handler http.Handler
	hashes  map[string]string
}

func newStaticAssets(files fs.FS) *staticAssets {
	s := &staticAssets{
		files:   files,
		handler: http.FileServer(http.FS(files)),
		hashes:  make(map[string]string),
	}
	// Hashing at startup rather than per request: the set is small,
	// fixed at build time, and this keeps the hot path free of I/O.
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

// URL returns the path a template should reference, carrying the
// content hash so a new build busts the cache without anyone having to
// remember to.
//
// Templates name assets by their public path ("/static/css/..."),
// while the map is keyed by their path inside the embedded sub-tree,
// which is what the handler sees once the prefix has been stripped.
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
			// The URL names this exact content, so it can never go
			// stale — a year is the conventional ceiling.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// Referenced without a version (a bookmark, a hand-typed
			// path): let it be cached but revalidated.
			w.Header().Set("Cache-Control", "public, no-cache")
		}
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, sum) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	s.handler.ServeHTTP(w, r)
}
