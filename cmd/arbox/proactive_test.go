package main

import (
	"testing"
	"time"

	"github.com/lafofo-nivo/arbox-scheduler/internal/config"
)

func TestNextActionableWindow_picksEarliestFutureOnly(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cfg := &config.Config{
		Timezone:    "Asia/Jerusalem",
		DefaultTime: "08:30",
		Days: map[string]config.DayConfig{
			"sunday":   {Enabled: true, Time: "08:00", Category: "Hall A"},
			"monday":   {Enabled: true, Time: "08:30", Category: "Hall B"},
			"tuesday":  {Enabled: true, Time: "09:00", Category: "Hall A"},
			"wednesday": {Enabled: false},
			"thursday": {Enabled: false},
			"friday":   {Enabled: false},
			"saturday": {Enabled: false},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	// Pick a Saturday so the next window is Sunday 08:00 - 48h = Friday 08:00.
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, loc)
	got, err := nextActionableWindow(cfg, now, 14)
	if err != nil {
		t.Fatal(err)
	}
	// Sunday 19 Apr 08:00 - 48h = Friday 17 Apr 08:00 — already past in this
	// scenario, so the earliest future window is Monday 20 Apr 08:30 - 24h =
	// Sunday 19 Apr 08:30.
	want := time.Date(2026, 4, 19, 8, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNextActionableWindow_zeroWhenNoOpts(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cfg := &config.Config{
		Timezone:    "Asia/Jerusalem",
		DefaultTime: "08:30",
		Days: map[string]config.DayConfig{
			"sunday":   {Enabled: false},
			"monday":   {Enabled: false},
			"tuesday":  {Enabled: false},
			"wednesday": {Enabled: false},
			"thursday": {Enabled: false},
			"friday":   {Enabled: false},
			"saturday": {Enabled: false},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, loc)
	got, err := nextActionableWindow(cfg, now, 14)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Fatalf("expected zero, got %v", got)
	}
}

func TestSleepOrDone_doneCancels(t *testing.T) {
	type ctx interface {
		Done() <-chan struct{}
		Err() error
	}
	_ = ctx(nil) // type doc only
	// This test only exercises the cancel path through a closed channel via
	// context.WithCancel.
	c, cancel := newCanceledCtx()
	defer cancel()
	ok := sleepOrDone(c, 10*time.Second)
	if ok {
		t.Fatal("expected sleepOrDone to return false after cancel")
	}
}

// newCanceledCtx returns a context that is already cancelled.
func newCanceledCtx() (canceledContext, func()) {
	ctx, cancel := canceledContextFromBackground()
	cancel()
	return ctx, func() {}
}
