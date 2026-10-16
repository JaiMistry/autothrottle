package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const dataDirHint = "check that the folder is not read-only"

// The tray icon reads it to show what AutoThrottle is doing.
const localStatusEnabled = true

// Not 8080, which is qBittorrent's default.
const listenAddr = ":7676"

const usage = `Usage:
  autothrottle                     run in this window (Ctrl+C stops it)
  autothrottle service install     register the Windows service
  autothrottle service uninstall   stop and remove it
  autothrottle service start       start it and wait until it is running
  autothrottle service stop        stop it and wait until it has stopped

The installer runs these; there is normally no need to.`

func main() {
	if len(os.Args) > 1 {
		os.Exit(command(os.Args[1:], os.Stdout, os.Stderr))
	}
	dir, err := dataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	inService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "detecting the service manager:", err)
		os.Exit(1)
	}
	if inService {
		if err := runService(dir); err != nil {
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err = run(ctx, logger, dir, listenAddr, os.Stdout)
	stop()
	if err != nil {
		logger.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// command runs one of the service subcommands, returning the exit code.
func command(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "service" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	actions := map[string]func() error{
		"install":   installService,
		"uninstall": uninstallService,
		"start":     startService,
		"stop":      stopService,
	}
	action, ok := actions[args[1]]
	if !ok {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err := action(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "service %s: done\n", args[1])
	return 0
}

// dataDir is C:\ProgramData\AutoThrottle: one place for the whole
// machine, so the service and an administrator's window share it.
func dataDir() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", fmt.Errorf("finding the ProgramData folder: %w", err)
	}
	return filepath.Join(base, "AutoThrottle"), nil
}

// notWritable is a window started without the rights the service has.
func notWritable() error {
	return errors.New("cannot write to it. AutoThrottle normally runs as its Windows service; to run it in a window instead, stop the service and use an administrator prompt")
}
