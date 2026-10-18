//go:build !windows

package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// healthcheck asks the app running beside it whether it is serving, and
// returns the exit code Docker reads: 0 healthy, 1 not.
func healthcheck(listenAddr string, stderr io.Writer) int {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/health")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "healthcheck:", err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintln(stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
