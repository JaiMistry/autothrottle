package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// Nobody but SYSTEM, Administrators, the service account and the owner
// can read the folder or anything in it.
func TestRestrictDataDirOnWindows(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "encryption.key")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restrictDataDir(dir); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"S-1-5-18": true, "S-1-5-32-544": true, "S-1-5-19": true}
	if user, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		allowed[user.User.Sid.String()] = true
	}
	for _, path := range []string{dir, key} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		if path == dir {
			control, _, err := sd.Control()
			if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
				t.Errorf("the folder still inherits from its parent (control %#x, %v)", control, err)
			}
		}
		sids, err := daclSIDs(sd)
		if err != nil {
			t.Fatal(err)
		}
		if len(sids) == 0 {
			t.Errorf("%s has no access entries: %s", filepath.Base(path), sd)
		}
		for _, sid := range sids {
			if !allowed[sid] {
				t.Errorf("%s grants access to %s: %s", filepath.Base(path), sid, sd)
			}
		}
	}
}

// daclSIDs lists the account each entry of a security descriptor's DACL
// applies to, in full S-1-... form.
func daclSIDs(sd *windows.SECURITY_DESCRIPTOR) ([]string, error) {
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	var sids []string
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return nil, err
		}
		sids = append(sids, (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String())
	}
	return sids, nil
}

// The service starts, serves, and stops cleanly when asked.
func TestServiceExecute(t *testing.T) {
	clearAuthEnv(t)
	var logs syncBuffer
	s := &service{logger: slog.New(slog.NewTextHandler(&logs, nil)), dataDir: t.TempDir(), addr: "127.0.0.1:0", out: io.Discard}
	requests := make(chan svc.ChangeRequest)
	status := make(chan svc.Status, 10)
	type result struct {
		specific bool
		code     uint32
	}
	done := make(chan result, 1)
	go func() {
		specific, code := s.Execute(nil, requests, status)
		done <- result{specific, code}
	}()

	waitState(t, status, svc.Running)
	deadline := time.Now().Add(10 * time.Second)
	for !listening.MatchString(logs.String()) {
		if time.Now().After(deadline) {
			t.Fatalf("never listened; log:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: svc.Status{State: svc.Running}}
	waitState(t, status, svc.Running)
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	waitState(t, status, svc.StopPending)
	select {
	case r := <-done:
		if r.specific || r.code != 0 {
			t.Errorf("a requested stop exited with (%v, %d); log:\n%s", r.specific, r.code, logs.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Execute did not return after Stop")
	}
}

// Failing to start is reported as a failure, so recovery restarts it.
func TestServiceExecuteReportsAFailedStart(t *testing.T) {
	clearAuthEnv(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &service{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), dataDir: file, addr: "127.0.0.1:0", out: io.Discard}
	specific, code := s.Execute(nil, make(chan svc.ChangeRequest), make(chan svc.Status, 10))
	if !specific || code == 0 {
		t.Errorf("a failed start exited with (%v, %d)", specific, code)
	}
}

func waitState(t *testing.T, status <-chan svc.Status, want svc.State) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case st := <-status:
			if st.State == want {
				return
			}
		case <-timeout:
			t.Fatalf("never reported state %d", want)
		}
	}
}

func TestOpenServiceLogKeepsThePreviousOne(t *testing.T) {
	dir := t.TempDir()
	first, err := openServiceLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = first.WriteString("first run\n")
	_ = first.Close()
	second, err := openServiceLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	old, err := os.ReadFile(filepath.Join(dir, logName+".old"))
	if err != nil || !bytes.Contains(old, []byte("first run")) {
		t.Errorf("the previous log was not kept: %q, %v", old, err)
	}
}

func TestCommandUsage(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"service"}, {"service", "explode"}} {
		var out, errOut bytes.Buffer
		if code := command(args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "Usage:") {
			t.Errorf("%v: code %d, stderr %q", args, code, errOut.String())
		}
	}
}

func TestDataDirIsInProgramData(t *testing.T) {
	dir, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Base(dir), "AutoThrottle") || !strings.Contains(strings.ToLower(dir), "programdata") {
		t.Errorf("data folder is %s", dir)
	}
}
