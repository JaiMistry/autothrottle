//go:build !windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Fixed: where it lives on the host is the bind mount's business.
const dataDir = "/data"

const dataDirHint = "check the bind mount for /data"

// The tray icon that reads it is Windows-only.
const localStatusEnabled = false

// Not in the container: a restart, as for an update, would lift a throttle
// mid-stream for the seconds it takes.
const releaseOnExit = false

// Not 8080, which is qBittorrent's default.
const listenAddr = ":7676"

func main() {
	// Docker's HEALTHCHECK, as root and beside the running app: the image
	// has no shell or curl to ask it with.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(listenAddr, os.Stderr))
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if err := dropPrivileges(dataDir); err != nil {
		logger.Error("switching to PUID/PGID", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, dataDir, listenAddr, os.Stdout)
	stop()
	if err != nil {
		logger.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// notWritable is usually a bind mount Docker created as root.
func notWritable() error {
	return fmt.Errorf("cannot write to it as uid %d, gid %d. If the container is started with user:, that user must own the mounted directory; otherwise check the mount is not read-only", os.Getuid(), os.Getgid())
}
