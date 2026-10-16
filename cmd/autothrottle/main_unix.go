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

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if err := dropPrivileges(dataDir); err != nil {
		logger.Error("switching to PUID/PGID", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// Not 8080, which is qBittorrent's default.
	err := run(ctx, logger, dataDir, ":7676", os.Stdout)
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
