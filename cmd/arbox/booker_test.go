package main

import (
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

func TestGroupOptionsBySlot_orderAndPriority(t *testing.T) {
	loc := time.UTC
	t1 := time.Date(2026, 4, 19, 8, 0, 0, 0, loc)
	t2 := time.Date(2026, 4, 20, 8, 30, 0, 0, loc)
	in := []schedule.PlannedOption{
		{ClassStart: t2, Priority: 1, Time: "08:30", Category: "Hall A"},
		{ClassStart: t1, Priority: 1, Time: "08:00", Category: "Hall A"},
		{ClassStart: t1, Priority: 0, Time: "08:00", Category: "Hall B"},
		{ClassStart: t2, Priority: 0, Time: "08:30", Category: "Hall B"},
	}
	got := groupOptionsBySlot(in)
	if len(got) != 2 {
		t.Fatalf("groups: %d", len(got))
	}
	if !got[0].ClassStart.Equal(t1) || !got[1].ClassStart.Equal(t2) {
		t.Fatalf("groups not sorted by start: %#v", got)
	}
	if got[0].Options[0].Priority != 0 || got[0].Options[1].Priority != 1 {
		t.Fatalf("priority order wrong inside slot 0: %#v", got[0].Options)
	}
	if got[1].Options[0].Category != "Hall B" {
		t.Fatalf("priority 0 should be Hall B in slot 1: %#v", got[1].Options)
	}
}

func TestAlreadyHoldsAtStart_byTime(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	start := time.Date(2026, 4, 19, 8, 0, 0, 0, loc)
	booked := 991
	classes := []arboxapi.Class{
		{ID: 1, Date: "2026-04-19", Time: "08:00", UserBookedID: &booked},
		{ID: 2, Date: "2026-04-19", Time: "08:00"},
	}
	if !alreadyHoldsAtStart(classes, start, loc, "2026-04-19") {
		t.Fatal("expected already-held")
	}
}

func TestAlreadyHoldsAtStart_noMatch(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	start := time.Date(2026, 4, 19, 8, 0, 0, 0, loc)
	booked := 991
	classes := []arboxapi.Class{
		{ID: 1, Date: "2026-04-19", Time: "09:00", UserBookedID: &booked},
		{ID: 2, Date: "2026-04-19", Time: "08:00"},
	}
	if alreadyHoldsAtStart(classes, start, loc, "2026-04-19") {
		t.Fatal("must not match — booking is at 09:00, not 08:00")
	}
}

func TestPruneAttempts(t *testing.T) {
	now := time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)
	s := attemptsState{Attempts: map[int]bookingAttempt{
		1: {ScheduleID: 1, When: now.Add(-40 * 24 * time.Hour)}, // expired
		2: {ScheduleID: 2, When: now.Add(-10 * 24 * time.Hour)}, // recent
		3: {ScheduleID: 3, When: now},                           // today
	}}
	pruneAttempts(&s, now)
	if _, ok := s.Attempts[1]; ok {
		t.Errorf("expected id 1 pruned")
	}
	if _, ok := s.Attempts[2]; !ok {
		t.Errorf("expected id 2 kept")
	}
	if _, ok := s.Attempts[3]; !ok {
		t.Errorf("expected id 3 kept")
	}
}

func TestAttemptsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARBOX_BOOKING_ATTEMPTS", dir+"/booking_attempts.json")
	in := attemptsState{Attempts: map[int]bookingAttempt{
		49049252: {ScheduleID: 49049252, Result: resultBooked, Slot: "2026-04-19 08:00 Hall A", When: time.Now()},
	}}
	if err := writeAttemptsState(in); err != nil {
		t.Fatal(err)
	}
	out := readAttemptsState()
	got, ok := out.Attempts[49049252]
	if !ok || got.Result != resultBooked {
		t.Fatalf("round-trip lost record: %+v", out)
	}
}
