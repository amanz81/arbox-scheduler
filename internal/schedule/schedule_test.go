package schedule

import (
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/config"
)

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatalf("load Asia/Jerusalem tz: %v", err)
	}
	return loc
}

// buildConfig returns a valid config whose Location() is resolved.
func buildConfig(t *testing.T) *config.Config {
	t.Helper()
	c := &config.Config{
		Timezone:    "Asia/Jerusalem",
		DefaultTime: "09:00",
		Days: map[string]config.DayConfig{
			"sunday":    {Enabled: true, Time: "08:00"},
			"monday":    {Enabled: true, Time: "08:30"},
			"tuesday":   {Enabled: true, Time: "09:00"},
			"wednesday": {Enabled: false},
			"thursday":  {Enabled: true, Time: "08:30"},
			"friday":    {Enabled: true, Time: "11:00"},
			"saturday":  {Enabled: false},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("config should validate: %v", err)
	}
	return c
}

// TestMondayWindowOpensSundayMorning: a 24h lead class on Monday 08:30 should
// have its booking window open Sunday 08:30, same local time.
func TestMondayWindow24h(t *testing.T) {
	loc := mustLoc(t)
	cfg := buildConfig(t)
	// Pick a Sunday anchor in summer (no DST boundary in the next 24h).
	from := time.Date(2026, 7, 5 /*Sunday*/, 0, 0, 0, 0, loc)

	bookings, err := NextOptions(cfg, from, 7)
	if err != nil {
		t.Fatal(err)
	}
	var mon *PlannedOption
	for i := range bookings {
		if bookings[i].Weekday == time.Monday {
			mon = &bookings[i]
			break
		}
	}
	if mon == nil {
		t.Fatal("expected a Monday booking in the next 7 days")
	}
	wantStart := time.Date(2026, 7, 6, 8, 30, 0, 0, loc)
	wantWindow := time.Date(2026, 7, 5, 8, 30, 0, 0, loc)
	if !mon.ClassStart.Equal(wantStart) {
		t.Errorf("class start: got %v want %v", mon.ClassStart, wantStart)
	}
	if !mon.WindowOpen.Equal(wantWindow) {
		t.Errorf("window open: got %v want %v", mon.WindowOpen, wantWindow)
	}
}

// TestSundayWindow48h: Sunday 08:00 class should have window opening Friday 08:00
// (48h earlier, same local time).
func TestSundayWindow48h(t *testing.T) {
	loc := mustLoc(t)
	cfg := buildConfig(t)
	// Anchor on a Friday morning before 08:00 so the Sunday class is captured.
	from := time.Date(2026, 7, 3 /*Friday*/, 0, 0, 0, 0, loc)

	bookings, err := NextOptions(cfg, from, 7)
	if err != nil {
		t.Fatal(err)
	}
	var sun *PlannedOption
	for i := range bookings {
		if bookings[i].Weekday == time.Sunday {
			sun = &bookings[i]
			break
		}
	}
	if sun == nil {
		t.Fatal("expected a Sunday booking in next 7 days")
	}
	wantStart := time.Date(2026, 7, 5, 8, 0, 0, 0, loc)
	wantWindow := time.Date(2026, 7, 3, 8, 0, 0, 0, loc)
	if !sun.ClassStart.Equal(wantStart) {
		t.Errorf("class start: got %v want %v", sun.ClassStart, wantStart)
	}
	if !sun.WindowOpen.Equal(wantWindow) {
		t.Errorf("window open: got %v want %v", sun.WindowOpen, wantWindow)
	}
	// The 48h rule means the gap is exactly 48h wall-clock (no DST between
	// Fri 08:00 and Sun 08:00 in early July).
	if got := sun.ClassStart.Sub(sun.WindowOpen); got != 48*time.Hour {
		t.Errorf("gap should be 48h, got %v", got)
	}
}

// TestDisabledDaySkipped: Wednesday and Saturday must never appear.
func TestDisabledDaySkipped(t *testing.T) {
	loc := mustLoc(t)
	cfg := buildConfig(t)
	from := time.Date(2026, 7, 5, 0, 0, 0, 0, loc)
	bookings, err := NextOptions(cfg, from, 14)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bookings {
		if b.Weekday == time.Wednesday || b.Weekday == time.Saturday {
			t.Errorf("disabled day %s appeared in bookings", b.Weekday)
		}
	}
}

// TestTodayExcludedIfClassAlreadyStarted: if called after today's class start,
// today's class should be skipped.
func TestTodayExcludedIfClassAlreadyStarted(t *testing.T) {
	loc := mustLoc(t)
	cfg := buildConfig(t)
	// Monday 09:00 — past Monday's 08:30 class.
	from := time.Date(2026, 7, 6, 9, 0, 0, 0, loc)
	bookings, err := NextOptions(cfg, from, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bookings {
		if b.Weekday == time.Monday {
			t.Errorf("today's class should be excluded, got %v", b.ClassStart)
		}
	}
}

// TestDSTSpringForward: Israel spring-forward in 2026 is Fri Mar 27 02:00 -> 03:00.
// A Sunday (Mar 29) class at 08:00 computed with 48h lead should land on Fri
// Mar 27 at 08:00 local time (same wall clock), even though wall-clock
// duration between those is 47h (a hour is lost).
func TestDSTSpringForwardSundayWindow(t *testing.T) {
	loc := mustLoc(t)
	// Israel 2026 DST start: Friday Mar 27 02:00 -> 03:00 local.
	cfg := buildConfig(t)

	// Anchor before the Friday window-open moment.
	from := time.Date(2026, 3, 26, 0, 0, 0, 0, loc)
	bookings, err := NextOptions(cfg, from, 7)
	if err != nil {
		t.Fatal(err)
	}
	var sun *PlannedOption
	for i := range bookings {
		if bookings[i].Weekday == time.Sunday {
			sun = &bookings[i]
			break
		}
	}
	if sun == nil {
		t.Fatal("expected Sunday booking")
	}
	wantStart := time.Date(2026, 3, 29, 8, 0, 0, 0, loc)
	if !sun.ClassStart.Equal(wantStart) {
		t.Errorf("sunday class start: got %v want %v", sun.ClassStart, wantStart)
	}
	// Window opens 48h before; wall-clock gap through spring-forward is 47h.
	expectedWindow := wantStart.Add(-48 * time.Hour)
	if !sun.WindowOpen.Equal(expectedWindow) {
		t.Errorf("window open: got %v want %v", sun.WindowOpen, expectedWindow)
	}
	if got := sun.ClassStart.Sub(sun.WindowOpen); got != 48*time.Hour {
		t.Errorf("gap must be exactly 48h in duration, got %v", got)
	}
}

// TestDSTFallBackSundayWindow: Israel fall-back 2026 is Sun Oct 25 02:00 -> 01:00.
// Sunday Oct 25 08:00 class has a 48h window, opening Fri Oct 23 at wall-clock
// time such that ClassStart-WindowOpen == 48h. Because an hour is gained
// crossing fall-back, wall-clock between the two endpoints is 49h if we picked
// "same local time". Using the 48h-duration rule, the window should land at
// Fri Oct 23 09:00 local.
func TestDSTFallBackSundayWindow(t *testing.T) {
	loc := mustLoc(t)
	cfg := buildConfig(t)

	from := time.Date(2026, 10, 22, 0, 0, 0, 0, loc)
	bookings, err := NextOptions(cfg, from, 7)
	if err != nil {
		t.Fatal(err)
	}
	var sun *PlannedOption
	for i := range bookings {
		if bookings[i].Weekday == time.Sunday {
			sun = &bookings[i]
			break
		}
	}
	if sun == nil {
		t.Fatal("expected Sunday booking")
	}
	wantStart := time.Date(2026, 10, 25, 8, 0, 0, 0, loc)
	if !sun.ClassStart.Equal(wantStart) {
		t.Errorf("sunday class start: got %v want %v", sun.ClassStart, wantStart)
	}
	if got := sun.ClassStart.Sub(sun.WindowOpen); got != 48*time.Hour {
		t.Errorf("gap must be exactly 48h, got %v", got)
	}
}

// TestPriorityOptions: a day with multiple options produces one entry per
// option, sharing the same ClassStart but different Priority values.
func TestPriorityOptions(t *testing.T) {
	loc := mustLoc(t)
	cfg := &config.Config{
		Timezone: "Asia/Jerusalem",
		Days: map[string]config.DayConfig{
			"monday": {
				Enabled: true,
				Options: []config.ClassOption{
					{Time: "08:30", Category: "Hall B"},
					{Time: "08:30", Category: "Hall A"},
					{Time: "09:00", Category: "Hall B"},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 5 /*Sunday*/, 0, 0, 0, 0, loc)
	got, err := NextOptions(cfg, from, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 options, got %d: %+v", len(got), got)
	}
	// First two share the same ClassStart (08:30), differ in priority.
	if !got[0].ClassStart.Equal(got[1].ClassStart) {
		t.Errorf("first two should share ClassStart: %v vs %v", got[0].ClassStart, got[1].ClassStart)
	}
	if got[0].Priority != 0 || got[1].Priority != 1 {
		t.Errorf("priorities: got[0]=%d got[1]=%d", got[0].Priority, got[1].Priority)
	}
	if got[0].Category != "Hall B" {
		t.Errorf("most-preferred category: %q", got[0].Category)
	}
	// Third option is 09:00, later ClassStart.
	if !got[2].ClassStart.After(got[1].ClassStart) {
		t.Errorf("third option should be after: %v", got[2].ClassStart)
	}
	if got[2].Priority != 2 {
		t.Errorf("third priority: %d", got[2].Priority)
	}
}

// TestOrdering: NextOptions should return bookings in chronological order.
func TestOrdering(t *testing.T) {
	loc := mustLoc(t)
	cfg := buildConfig(t)
	from := time.Date(2026, 7, 5, 0, 0, 0, 0, loc)
	bookings, err := NextOptions(cfg, from, 7)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(bookings); i++ {
		if bookings[i].ClassStart.Before(bookings[i-1].ClassStart) {
			t.Errorf("bookings not sorted at %d", i)
		}
	}
}
