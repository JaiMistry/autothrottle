//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "autothrottle-tray is the Windows tray icon; it only runs on Windows.")
	os.Exit(1)
}
