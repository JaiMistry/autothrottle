// Command autothrottle-tray is the Windows tray icon. It shows what the
// AutoThrottle service is doing and opens its page; the service does the
// work whether or not the icon is running. Exit stops both.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// status is what the service's /local/status answers.
type status struct {
	Setup     bool   `json:"setup"`
	Statement string `json:"statement"`
	Ready     bool   `json:"ready"`
	Paused    bool   `json:"paused"`
	Sessions  int    `json:"sessions"`
	Throttled int    `json:"throttled"`
}

func fetch(client *http.Client, url string) (status, error) {
	var st status
	resp, err := client.Get(url)
	if err != nil {
		return st, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("answered %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&st)
	return st, err
}

// describe is the tooltip's status line, and whether AutoThrottle answered.
func describe(st status, err error) (string, bool) {
	switch {
	case err != nil:
		return "AutoThrottle isn't running", false
	case st.Setup:
		return "Open AutoThrottle to finish setting up", true
	case st.Paused:
		return "Throttling is turned off", true
	}
	line := st.Statement
	switch {
	case st.Sessions == 1:
		line += " · 1 stream playing"
	case st.Sessions > 1:
		line += fmt.Sprintf(" · %d streams playing", st.Sessions)
	}
	return line, true
}
