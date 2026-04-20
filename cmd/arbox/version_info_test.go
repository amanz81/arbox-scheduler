package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionTemplate_IncludesRevAndGoAndArch(t *testing.T) {
	got := versionTemplate()

	// Must start with "arbox " so `arbox --version` parses cleanly.
	if !strings.HasPrefix(got, "arbox ") {
		t.Errorf("version output must start with 'arbox ', got %q", got)
	}
	// The Version var (set via -ldflags at build time, "dev" in tests) must
	// appear verbatim — that's what ops scripts grep for to verify the
	// running binary matches a known commit.
	if !strings.Contains(got, Version) {
		t.Errorf("version output missing Version=%q, got %q", Version, got)
	}
	// Go version is useful when debugging runtime-specific issues.
	if !strings.Contains(got, runtime.Version()) {
		t.Errorf("version output missing Go version, got %q", got)
	}
	// OS/arch prevents "I built darwin/arm64 locally but shipped linux/amd64"
	// confusion.
	if !strings.Contains(got, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("version output missing GOOS/GOARCH, got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("version output must end with newline so shell prompts don't run on, got %q", got)
	}
}
