package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/config"
)

// buildVersionReport returns a short multi-line string for /version.
// It surfaces the deployed git revision (when injected via -ldflags or via
// runtime/debug.ReadBuildInfo) along with the gym binding and timezone — the
// most common things you'll want to confirm after a Fly deploy.
func buildVersionReport(cfg *config.Config, locID, lookahead int) string {
	loc := cfg.Location()
	now := time.Now().In(loc)

	rev, modified := buildRevision()
	revStr := rev
	if modified {
		revStr += " (dirty)"
	}
	if revStr == "" {
		revStr = "unknown"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s\n", now.Format("Mon 02 Jan 15:04 MST"))
	fmt.Fprintf(&b, "Build: %s · rev %s\n", Version, revStr)
	fmt.Fprintf(&b, "Gym: %s\n", strDefault(cfg.Gym, "(unset — picks first box)"))
	fmt.Fprintf(&b, "locations_box_id: %d\n", locID)
	if envBox := os.Getenv("ARBOX_BOX_ID"); envBox != "" {
		if n, err := strconv.Atoi(envBox); err == nil {
			fmt.Fprintf(&b, "box_id: %d\n", n)
		}
	}
	fmt.Fprintf(&b, "Timezone: %s\n", cfg.Timezone)
	fmt.Fprintf(&b, "Lookahead: %d days\n", lookahead)

	if ps, err := readPauseState(); err == nil {
		if tag := ps.Summary(now, loc); tag != "" {
			fmt.Fprintf(&b, "%s\n", tag)
		} else {
			b.WriteString("Auto-booking: enabled\n")
		}
	}
	return b.String()
}

func strDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func buildRevision() (rev string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				rev = s.Value[:7]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return rev, modified
}
