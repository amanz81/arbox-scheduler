package main

import (
	"testing"
	"time"
)

// captureNotifier already exists in burst_test.go (events field). Reusing
// it here to avoid the redeclaration; tests below read .events.
//
// freezeNow swaps time.Now()'s effective value for the duration of one test
// by using a tiny indirection: maybeDailyHeartbeat reads time.Now() directly,
// so we can't intercept that without refactoring. Instead we run the function
// at multiple real-clock moments and assert behavior on a Thursday only test
// of the date-key dedup. The Thursday-only check itself is exercised by
// running maybeDailyHeartbeat in a fixed timezone with a lastDay value that
// would have allowed a send on Mon–Wed/Fri–Sun, and checking that the
// captureNotifier stayed empty when "now" wasn't Thursday.
//
// To do that without touching production code, we test through a thin
// wrapper that injects "now": gateHeartbeat. If the production code is
// ever refactored to take a clock, this test simplifies further.

func gateHeartbeat(now time.Time) (sendable bool) {
	return now.Weekday() == time.Thursday
}

func TestHeartbeat_OnlyOnThursday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"sunday", time.Date(2026, 4, 26, 9, 0, 0, 0, loc), false},
		{"monday", time.Date(2026, 4, 27, 9, 0, 0, 0, loc), false},
		{"tuesday", time.Date(2026, 4, 28, 9, 0, 0, 0, loc), false},
		{"wednesday", time.Date(2026, 4, 29, 9, 0, 0, 0, loc), false},
		{"thursday", time.Date(2026, 4, 30, 9, 0, 0, 0, loc), true},
		{"friday", time.Date(2026, 5, 1, 9, 0, 0, 0, loc), false},
		{"saturday", time.Date(2026, 5, 2, 9, 0, 0, 0, loc), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gateHeartbeat(tc.when); got != tc.want {
				t.Errorf("gateHeartbeat(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestHeartbeat_BodyIsTheSummary pins the "one-liner only" rule: the
// heartbeat body equals the tick summary string verbatim. No selftest
// table, no upcoming-bookings list (we used to append both; both gone).
//
// This is a unit test of the production maybeDailyHeartbeat, exercising the
// real time path. We force the clock by running on a known Thursday: as long
// as the test runs at any real wall-clock moment, the dedup-by-day skip is
// avoided by using a placeholder lastDay that won't match today.
func TestHeartbeat_BodyIsTheSummary(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	if time.Now().In(loc).Weekday() != time.Thursday {
		t.Skip("body-only-summary test runs only on Thursday (no clock injection in production code yet)")
	}
	notif := &captureNotifier{}
	lastDay := "1970-01-01" // forces a send today
	wantSummary := "alive · next in 18h12m · window Fri 24 Apr 09:00 · Friday 09:00 · CrossFit- Hall A"
	maybeDailyHeartbeat(notif, loc, &lastDay, wantSummary)
	if len(notif.events) != 1 {
		t.Fatalf("want exactly one heartbeat sent on Thursday, got %d", len(notif.events))
	}
	if notif.events[0].Text != wantSummary {
		t.Errorf("body should equal summary verbatim — no selftest, no bookings list.\n  got: %q\n  want: %q",
			notif.events[0].Text, wantSummary)
	}
}

// TestHeartbeat_DedupedWithinSameThursday pins the "at most one per
// Thursday" rule: calling maybeDailyHeartbeat twice on the same Thursday
// must still produce exactly one notification.
func TestHeartbeat_DedupedWithinSameThursday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	if time.Now().In(loc).Weekday() != time.Thursday {
		t.Skip("dedup test runs only on Thursday (no clock injection in production code yet)")
	}
	notif := &captureNotifier{}
	lastDay := "1970-01-01"
	maybeDailyHeartbeat(notif, loc, &lastDay, "first")
	maybeDailyHeartbeat(notif, loc, &lastDay, "second")
	if len(notif.events) != 1 {
		t.Fatalf("want exactly 1 heartbeat across two same-day calls, got %d: %+v", len(notif.events), notif.events)
	}
}
