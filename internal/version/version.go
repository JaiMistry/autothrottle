// Package version carries the build's identity. Set at link time; the
// fallback is what a plain `go build` or `go run` produces, which is
// exactly when "dev" is the honest answer.
package version

// Version is overridden with -ldflags "-X .../internal/version.Version=1.2.3".
var Version = "dev"

// Commit is the short revision the binary was built from, when known.
var Commit = ""
