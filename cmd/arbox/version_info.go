package main

import (
	"fmt"
	"runtime"
)

// versionTemplate is what `arbox --version` prints. Kept minimal and
// machine-parseable so CI and scripts can diff it ("is Oracle running what
// main has?") without pulling the full /version Telegram output.
//
// Example:
//
//	arbox 213aba1 go1.24.0 linux/amd64
//
// When built without `-ldflags "-X main.Version=..."` Version defaults to
// "dev", which is fine for local builds but surfaces clearly in logs.
func versionTemplate() string {
	return fmt.Sprintf("arbox %s %s %s/%s\n",
		Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
