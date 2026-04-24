package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lafofo-nivo/arbox-scheduler/internal/config"
)

func TestNextPlannedBookingsSummary_oneLinePerDay(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cfg := &config.Config{
		Timezone:    "Asia/Jerusalem",
		DefaultTime: "08:30",
		Days: map[string]config.DayConfig{
			// Priority-list input is legacy and now clamped: Hall B wins,
			// Hall A dropped. The summary line reflects only Hall B.
			"sunday": {Enabled: true, Options: []config.ClassOption{
				{Time: "08:00", Category: "Hall B"},
				{Time: "08:00", Category: "Hall A"},
			}},
			"monday": {Enabled: true, Options: []config.ClassOption{
				{Time: "08:30", Category: "Hall A"},
			}},
			"tuesday":   {Enabled: false},
			"wednesday": {Enabled: false},
			"thursday":  {Enabled: false},
			"friday":    {Enabled: false},
			"saturday":  {Enabled: false},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	got := nextPlannedBookingsSummary(cfg, 14, 5)
	if len(got) == 0 {
		t.Fatal("expected at least one upcoming line")
	}
	joined := strings.Join(got, "\n")
	// Sundays must render as Hall B alone — the legacy "Hall B then Hall A"
	// collapse is gone because priority fallback is gone.
	if strings.Contains(joined, "Hall B then Hall A") {
		t.Fatalf("legacy priority concatenation should be gone:\n%s", joined)
	}
	if !strings.Contains(joined, "Hall B") {
		t.Fatalf("Sunday Hall B line missing:\n%s", joined)
	}
	for _, l := range got {
		if !strings.Contains(l, "window") {
			t.Fatalf("missing window: %q", l)
		}
	}
	_ = loc
}

func TestFormatSelfTestReport_passFailHeader(t *testing.T) {
	checks := []selfCheck{
		{Name: "alpha", OK: true, Detail: "ok", Latency: 5 * time.Millisecond},
		{Name: "beta", OK: false, Detail: "boom", Latency: 12 * time.Millisecond},
	}
	got := formatSelfTestReport(checks)
	if !strings.Contains(got, "1 passed, 1 failed") {
		t.Fatalf("header wrong: %q", got)
	}
	if !strings.Contains(got, "✓ alpha") || !strings.Contains(got, "✗ beta [12ms] boom") {
		t.Fatalf("rows wrong:\n%s", got)
	}
}
