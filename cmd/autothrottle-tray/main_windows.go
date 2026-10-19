package main

import (
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

// The mark without its tile, enlarged: the tile is near black and
// vanishes on a dark taskbar, leaving the mark smaller than its
// neighbours.
//
//go:embed tray.ico
var iconRunning []byte

// Greyed out: the service isn't answering.
//
//go:embed tray-off.ico
var iconStopped []byte

const (
	pageURL       = "http://localhost:7676/"
	aboutURL      = "http://localhost:7676/configuration?tab=about"
	statusURL     = "http://127.0.0.1:7676/local/status"
	throttlingURL = "http://127.0.0.1:7676/local/throttling"

	serviceName = "AutoThrottle"
)

func main() {
	// The Start menu shortcut: AutoThrottle and its icon back if they were
	// exited, then the page. Only then: the icon is optional at install.
	if len(os.Args) > 1 && os.Args[1] == "open" {
		if startService() {
			if exe, err := os.Executable(); err == nil {
				_ = shellExecute("open", exe, "", windows.SW_SHOWNORMAL)
			}
		}
		openPage(pageURL)
		return
	}
	// One icon per signed-in user, however many times it is started. The
	// handle stays open until the process ends.
	name, err := windows.UTF16PtrFromString(`Local\AutoThrottleTray`)
	if err != nil {
		return
	}
	if _, err := windows.CreateMutex(nil, false, name); errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return
	}
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetIcon(iconStopped)
	systray.SetTooltip("AutoThrottle")
	// A click opens the page; a right-click shows the menu.
	systray.SetOnTapped(func() { go open(pageURL) })
	// Named for what a click does, as the page's button is. Greyed when
	// the page wouldn't offer it either: AutoThrottle isn't answering, or
	// has nothing to throttle yet and isn't already off.
	toggle := systray.AddMenuItem("Disable throttling", "")
	toggle.Disable()
	systray.AddSeparator()
	update := systray.AddMenuItem("Check for updates", "Open About, where you can check for a newer release")
	exit := systray.AddMenuItem("Exit", "Stop AutoThrottle, restoring download speeds, and close this icon")
	var paused atomic.Bool
	refresh := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-toggle.ClickedCh:
				go func() {
					_ = setPaused(!paused.Load())
					select {
					case refresh <- struct{}{}:
					default:
					}
				}()
			case <-update.ClickedCh:
				go open(aboutURL)
			case <-exit.ClickedCh:
				if stopService() {
					systray.Quit()
					return
				}
			}
		}
	}()
	go startService()
	go watch(toggle, &paused, refresh)
}

// watch keeps the icon, its tooltip and the throttling item current,
// every few seconds or as soon as refresh says something changed.
func watch(toggle *systray.MenuItem, paused *atomic.Bool, refresh <-chan struct{}) {
	client := &http.Client{Timeout: 3 * time.Second}
	lastText, lastLabel, lastUp, lastCan, first := "", "", false, false, true
	for {
		st, err := fetch(client, statusURL)
		text, up := describe(st, err)
		if first || text != lastText {
			systray.SetTooltip("AutoThrottle: " + text)
			lastText = text
		}
		if first || up != lastUp {
			if up {
				systray.SetIcon(iconRunning)
			} else {
				systray.SetIcon(iconStopped)
			}
			lastUp = up
		}
		paused.Store(st.Paused)
		label := "Disable throttling"
		if st.Paused {
			label = "Resume throttling"
		}
		if label != lastLabel {
			toggle.SetTitle(label)
			lastLabel = label
		}
		if can := up && !st.Setup && (st.Ready || st.Paused); first || can != lastCan {
			if can {
				toggle.Enable()
			} else {
				toggle.Disable()
			}
			lastCan = can
		}
		first = false
		select {
		case <-refresh:
		case <-time.After(5 * time.Second):
		}
	}
}

// setPaused turns throttling off, or back on, as the page's button does.
func setPaused(paused bool) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(throttlingURL, url.Values{"paused": {strconv.FormatBool(paused)}})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	return nil
}

// open starts AutoThrottle if it was exited, then opens url.
func open(url string) {
	startService()
	openPage(url)
}

func openPage(url string) {
	_ = shellExecute("open", url, "", windows.SW_SHOWNORMAL)
}

func shellExecute(verb, file, args string, show int32) error {
	v, err := windows.UTF16PtrFromString(verb)
	if err != nil {
		return err
	}
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var a *uint16
	if args != "" {
		if a, err = windows.UTF16PtrFromString(args); err != nil {
			return err
		}
	}
	return windows.ShellExecute(0, v, f, a, nil, show)
}

// openService opens the service with the given rights, which the
// installer grants everyone signed in for starting and stopping it.
func openService(access uint32) (windows.Handle, func(), error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, nil, err
	}
	name, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		_ = windows.CloseServiceHandle(m)
		return 0, nil, err
	}
	s, err := windows.OpenService(m, name, access|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		_ = windows.CloseServiceHandle(m)
		return 0, nil, err
	}
	return s, func() {
		_ = windows.CloseServiceHandle(s)
		_ = windows.CloseServiceHandle(m)
	}, nil
}

// waitFor polls the service until it reaches state, or gives up.
func waitFor(s windows.Handle, state uint32, limit time.Duration) bool {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		var st windows.SERVICE_STATUS
		if windows.QueryServiceStatus(s, &st) != nil {
			return false
		}
		if st.CurrentState == state {
			return true
		}
	}
	return false
}

// startService starts the service if it is stopped, as after Exit, and
// waits for the page to answer; it reports whether it had to. Running
// already is the usual case.
func startService() bool {
	s, done, err := openService(windows.SERVICE_START)
	if err != nil {
		return false
	}
	defer done()
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(s, &st) != nil || st.CurrentState == windows.SERVICE_RUNNING {
		return false
	}
	if st.CurrentState == windows.SERVICE_STOP_PENDING {
		waitFor(s, windows.SERVICE_STOPPED, 30*time.Second)
	}
	if err := windows.StartService(s, 0, nil); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return false
	}
	client := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if _, err := fetch(client, statusURL); err == nil {
			break
		}
	}
	return true
}

// stopService stops the service, which restores every client's default
// speeds on its way out. Without the right to, it asks Windows to run the
// service's own stop as administrator. It reports whether AutoThrottle
// is stopping, so the icon can go.
func stopService() bool {
	s, done, err := openService(windows.SERVICE_STOP)
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		exe, err := os.Executable()
		if err != nil {
			return false
		}
		// Declined at the prompt: AutoThrottle keeps running, and so does
		// the icon.
		return shellExecute("runas", filepath.Join(filepath.Dir(exe), "autothrottle.exe"), "service stop", windows.SW_HIDE) == nil
	case err != nil:
		return true // not installed: nothing to stop
	}
	defer done()
	var st windows.SERVICE_STATUS
	if err := windows.ControlService(s, windows.SERVICE_CONTROL_STOP, &st); err != nil &&
		!errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return false
	}
	waitFor(s, windows.SERVICE_STOPPED, 30*time.Second)
	return true
}
