package main

import (
	_ "embed"
	"errors"
	"net/http"
	"os"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

//go:embed autothrottle.ico
var iconRunning []byte

// Greyed out: the service isn't answering.
//
//go:embed autothrottle-off.ico
var iconStopped []byte

const (
	pageURL   = "http://localhost:7676/"
	statusURL = "http://127.0.0.1:7676/local/status"
)

func main() {
	// The Start menu shortcut: open the page and nothing else.
	if len(os.Args) > 1 && os.Args[1] == "open" {
		openPage()
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
	line := systray.AddMenuItem("Checking…", "")
	line.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem("Open AutoThrottle", "Open the dashboard in your browser")
	hide := systray.AddMenuItem("Hide icon", "AutoThrottle keeps running; the icon is back when you next sign in")
	go func() {
		for {
			select {
			case <-open.ClickedCh:
				openPage()
			case <-hide.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
	go watch(line)
}

// watch keeps the icon and the status line current.
func watch(line *systray.MenuItem) {
	client := &http.Client{Timeout: 3 * time.Second}
	lastText, lastUp, first := "", false, true
	for {
		text, up := describe(fetch(client, statusURL))
		if first || text != lastText {
			line.SetTitle(text)
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
		first = false
		time.Sleep(5 * time.Second)
	}
}

func openPage() {
	verb, _ := windows.UTF16PtrFromString("open")
	url, _ := windows.UTF16PtrFromString(pageURL)
	_ = windows.ShellExecute(0, verb, url, nil, nil, windows.SW_SHOWNORMAL)
}
