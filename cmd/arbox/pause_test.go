package main

import (
	"testing"
	"time"
)

func ilLocation(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatalf("load IL tz: %v", err)
	}
	return loc
}

func TestParsePauseArgs_defaults24h(t *testing.T) {
	loc := ilLocation(t)
	now := time.Date(2026, 4, 18, 20, 0, 0, 0, loc)
	until, reason, err := parsePauseArgs(nil, now, loc)
	if err != nil || reason != "" {
		t.Fatalf("err=%v reason=%q", err, reason)
	}
	if got := until.Sub(now); got != 24*time.Hour {
		t.Fatalf("default duration = %v, want 24h", got)
	}
}

func TestParsePauseArgs_durations(t *testing.T) {
	loc := ilLocation(t)
	now := time.Date(2026, 4, 18, 20, 0, 0, 0, loc)
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"3d", 72 * time.Hour},
		{"12h", 12 * time.Hour},
		{"90m", 90 * time.Minute},
	}
	for _, tc := range cases {
		until, _, err := parsePauseArgs([]string{tc.in}, now, loc)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got := until.Sub(now); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestParsePauseArgs_untilDate(t *testing.T) {
	loc := ilLocation(t)
	now := time.Date(2026, 4, 18, 20, 0, 0, 0, loc)
	until, reason, err := parsePauseArgs([]string{"until", "2026-04-25", "vacation", "trip"}, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantUntil := time.Date(2026, 4, 25, 23, 59, 0, 0, loc)
	if !until.Equal(wantUntil) {
		t.Errorf("until: got %v want %v", until, wantUntil)
	}
	if reason != "vacation trip" {
		t.Errorf("reason: %q", reason)
	}
}

func TestParsePauseArgs_untilDateTime(t *testing.T) {
	loc := ilLocation(t)
	now := time.Date(2026, 4, 18, 20, 0, 0, 0, loc)
	until, _, err := parsePauseArgs([]string{"until", "2026-04-25", "08:30"}, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if until.Hour() != 8 || until.Minute() != 30 {
		t.Errorf("until time: %v", until)
	}
}

func TestParsePauseArgs_pastUntilRejected(t *testing.T) {
	loc := ilLocation(t)
	now := time.Date(2026, 4, 18, 20, 0, 0, 0, loc)
	if _, _, err := parsePauseArgs([]string{"until", "2026-04-10"}, now, loc); err == nil {
		t.Fatal("expected error for past 'until'")
	}
}

func TestParsePauseArgs_badDuration(t *testing.T) {
	loc := ilLocation(t)
	now := time.Date(2026, 4, 18, 20, 0, 0, 0, loc)
	if _, _, err := parsePauseArgs([]string{"forever"}, now, loc); err == nil {
		t.Fatal("expected error for nonsense duration")
	}
}

func TestPauseState_IsActive(t *testing.T) {
	loc := ilLocation(t)
	now := time.Date(2026, 4, 18, 20, 0, 0, 0, loc)
	if (pauseState{}).IsActive(now) {
		t.Fatal("zero state should not be active")
	}
	if !(pauseState{PausedUntil: now.Add(time.Hour)}).IsActive(now) {
		t.Fatal("future until should be active")
	}
	if (pauseState{PausedUntil: now.Add(-time.Hour)}).IsActive(now) {
		t.Fatal("past until should not be active")
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Minute:                "45m",
		2 * time.Hour:                   "2h",
		(2*time.Hour + 30*time.Minute):  "2h30m",
		25 * time.Hour:                  "1d1h",
		(48 * time.Hour):                "2d",
	}
	for d, want := range cases {
		if got := shortDuration(d); got != want {
			t.Errorf("%v -> %q, want %q", d, got, want)
		}
	}
}
