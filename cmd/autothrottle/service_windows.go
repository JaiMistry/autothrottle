package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName = "AutoThrottle"
	// The least-privileged built-in account that can still reach the
	// network: the service needs nothing from the machine beyond its own
	// folder, which it is given.
	serviceAccount = `NT AUTHORITY\LocalService`
	logName        = "autothrottle.log"
	// How long start and stop wait for the service to get there.
	serviceTimeout = 30 * time.Second
)

// runService runs under the service manager. There is no console, so the
// banner and the log go to a file in the data folder.
func runService(dir string) error {
	logFile, err := openServiceLog(dir)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	logger := slog.New(slog.NewTextHandler(logFile, nil))
	err = svc.Run(serviceName, &service{logger: logger, dataDir: dir, addr: listenAddr, out: logFile})
	if err != nil {
		logger.Error("running as a service", "error", err)
	}
	return err
}

// openServiceLog starts a fresh log for this run, keeping the previous
// one beside it, so a crash's log survives the restart that follows it.
func openServiceLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := restrictDataDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, logName)
	if err := os.Rename(path, path+".old"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// service is AutoThrottle under the Windows service manager.
type service struct {
	logger  *slog.Logger
	dataDir string
	addr    string
	out     io.Writer
}

// Execute runs until the service manager asks it to stop. Stopping on
// its own is reported as a failure, so the recovery actions restart it.
func (s *service) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, s.logger, s.dataDir, s.addr, s.out) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case err := <-done:
			s.logger.Error("exiting", "error", err)
			return true, 1
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// run's own shutdown takes up to ten seconds.
				status <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
				if err := <-done; err != nil {
					s.logger.Error("stopping", "error", err)
					return true, 2
				}
				return false, 0
			}
		}
	}
}

// installService registers the service, or brings an existing one up to
// date (an upgrade), and gives it its data folder.
func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	// Made here, as an administrator, so the service account is given
	// the folder rather than having to be able to create it.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := restrictDataDir(dir); err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (run as administrator): %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	config := mgr.Config{
		DisplayName:      "AutoThrottle",
		Description:      "Throttles download clients while a media server is playing. Manage it at http://localhost:7676.",
		StartType:        mgr.StartAutomatic,
		ServiceStartName: serviceAccount,
	}
	s, err := m.OpenService(serviceName)
	if err == nil {
		current, err := s.Config()
		if err != nil {
			_ = s.Close()
			return err
		}
		config.ServiceType = current.ServiceType
		config.ErrorControl = current.ErrorControl
		config.BinaryPathName = `"` + exe + `"`
		if err := s.UpdateConfig(config); err != nil {
			_ = s.Close()
			return fmt.Errorf("updating the service: %w", err)
		}
	} else {
		s, err = m.CreateService(serviceName, exe, config)
		if err != nil {
			return fmt.Errorf("creating the service: %w", err)
		}
	}
	defer func() { _ = s.Close() }()

	// Restart after a crash, backing off, and count a day without one as
	// a clean slate.
	restart := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}
	if err := s.SetRecoveryActions(restart, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("setting the restart policy: %w", err)
	}
	// Execute reports stopping on its own as a failure, not a crash.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("setting the restart policy: %w", err)
	}
	return nil
}

// uninstallService stops and removes the service; not installed is done.
func uninstallService() error {
	m, s, err := openService()
	if err != nil || s == nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	defer func() { _ = s.Close() }()
	if err := stop(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("removing the service: %w", err)
	}
	return nil
}

// startService waits until the service is running and still is a few
// seconds later: a port already in use stops it straight away.
func startService() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	if s == nil {
		return errors.New("the AutoThrottle service is not installed")
	}
	defer func() { _ = m.Disconnect() }()
	defer func() { _ = s.Close() }()

	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State != svc.Running {
		if err := s.Start(); err != nil {
			return fmt.Errorf("starting the service: %w", err)
		}
		if err := waitFor(s, svc.Running); err != nil {
			return err
		}
	}
	time.Sleep(3 * time.Second)
	if st, err = s.Query(); err != nil {
		return err
	}
	if st.State != svc.Running {
		dir, _ := dataDir()
		return fmt.Errorf("the service stopped after starting; see %s", filepath.Join(dir, logName))
	}
	return nil
}

func stopService() error {
	m, s, err := openService()
	if err != nil || s == nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	defer func() { _ = s.Close() }()
	return stop(s)
}

// openService returns a nil service, and no error, when it isn't installed.
func openService() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to the service manager (run as administrator): %w", err)
	}
	s, err := m.OpenService(serviceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		_ = m.Disconnect()
		return nil, nil, nil
	}
	if err != nil {
		_ = m.Disconnect()
		return nil, nil, err
	}
	return m, s, nil
}

func stop(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stopping the service: %w", err)
		}
	}
	return waitFor(s, svc.Stopped)
}

// waitFor polls until the service reaches want. Reaching Stopped while
// waiting for Running is a failure to start, reported at once.
func waitFor(s *mgr.Service, want svc.State) error {
	deadline := time.Now().Add(serviceTimeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if want == svc.Running && st.State == svc.Stopped {
			dir, _ := dataDir()
			return fmt.Errorf("the service stopped while starting; see %s", filepath.Join(dir, logName))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the service did not reach the %s state within %s", stateName(want), serviceTimeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func stateName(s svc.State) string {
	if s == svc.Running {
		return "running"
	}
	return "stopped"
}
