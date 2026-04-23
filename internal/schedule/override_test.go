package schedule

import (
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/config"
)

// TestNextOptions_OneTimeOverrideTakesPrecedence pins the key behavior:
// when a date has a matching override in cfg.OneTimeOverrides, NextOptions
// must emit the override's class times+categories instead of the weekday
// plan's — without disturbing any other date.
func TestNextOptions_OneTimeOverrideTakesPrecedence(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cfg := &config.Config{
		Timezone: "Asia/Jerusalem",
		Days: map[string]config.DayConfig{
			"sunday": {
				Enabled: true,
				Options: []config.ClassOption{{Time: "08:00", Category: "Hall B"}},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.OneTimeOverrides = config.OneTimeOverrides{
		"2026-04-26": { // next Sunday — overrides to Hall A at 09:00
			Enabled: true,
			Options: []config.ClassOption{{Time: "09:00", Category: "Hall A"}},
		},
	}

	from := time.Date(2026, 4, 24, 6, 0, 0, 0, loc) // Friday morning
	opts, err := NextOptions(cfg, from, 14)
	if err != nil {
		t.Fatal(err)
	}

	// Collect Sundays in the output.
	var sundays []PlannedOption
	for _, o := range opts {
		if o.Weekday == time.Sunday {
			sundays = append(sundays, o)
		}
	}
	if len(sundays) < 2 {
		t.Fatalf("expected ≥2 Sundays in 14-day window, got %d", len(sundays))
	}
	// Sundays[0] is 2026-04-26 → the overridden one.
	if sundays[0].ClassStart.Day() != 26 {
		t.Fatalf("first Sunday should be 26 Apr, got %v", sundays[0].ClassStart)
	}
	if sundays[0].Category != "Hall A" || sundays[0].Time != "09:00" {
		t.Errorf("override not applied on 26 Apr: got %+v", sundays[0])
	}
	// Sundays[1] is 2026-05-03 → should still be the weekday default (Hall B 08:00).
	if sundays[1].Category != "Hall B" || sundays[1].Time != "08:00" {
		t.Errorf("2026-05-03 should fall back to weekday plan, got %+v", sundays[1])
	}
}

func TestNextOptions_OverrideDisabledMakesRestDay(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cfg := &config.Config{
		Timezone: "Asia/Jerusalem",
		Days: map[string]config.DayConfig{
			"tuesday": {
				Enabled: true,
				Options: []config.ClassOption{{Time: "09:00", Category: "Weightlifting"}},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.OneTimeOverrides = config.OneTimeOverrides{
		"2026-04-28": {Enabled: false}, // "skip Tuesday — I'm traveling"
	}

	from := time.Date(2026, 4, 27, 6, 0, 0, 0, loc) // Monday morning
	opts, err := NextOptions(cfg, from, 10)
	if err != nil {
		t.Fatal(err)
	}

	for _, o := range opts {
		if o.Weekday == time.Tuesday && o.ClassStart.Day() == 28 {
			t.Errorf("disabled override should produce no Tuesday 28 options, got %+v", o)
		}
	}
	// But the following Tuesday (2026-05-05) should still be there.
	nextTue := false
	for _, o := range opts {
		if o.Weekday == time.Tuesday && o.ClassStart.Day() == 5 {
			nextTue = true
			break
		}
	}
	if !nextTue {
		t.Errorf("subsequent Tuesday should still be scheduled")
	}
}
