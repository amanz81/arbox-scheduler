package main

import (
	"strings"
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
)

// TestWriteUserBookingsSection_SurfacesWaitlistPosition is the regression guard
// for the /status fix: when the user is on a waitlist and Arbox returned
// StandByPosition, the rendered line must show "WAITLIST N/M" (not just
// "WAITLIST"). Before the fix, /status never listed bookings at all, so this
// code path was never exercised from the bot.
func TestWriteUserBookingsSection_SurfacesWaitlistPosition(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	booked := 42
	standby := 999

	bookedClass := arboxapi.Class{
		ID:           100,
		Date:         "2026-04-21",
		Time:         "08:30",
		MaxUsers:     15,
		Registered:   15,
		UserBookedID: &booked,
	}
	bookedClass.BoxCategories.Name = "CrossFit Hall B"

	waitlistClass := arboxapi.Class{
		ID:              200,
		Date:            "2026-04-22",
		Time:            "09:00",
		MaxUsers:        18,
		Registered:      18,
		StandBy:         7,
		StandByPosition: 3,
		UserStandByID:   &standby,
	}
	waitlistClass.BoxCategories.Name = "CrossFit Hall A"

	// Unrelated class the user isn't registered on — must not appear.
	otherClass := arboxapi.Class{
		ID:       300,
		Date:     "2026-04-21",
		Time:     "10:00",
		MaxUsers: 10,
		Free:     5,
	}
	otherClass.BoxCategories.Name = "Open Box"

	allBy := map[string][]arboxapi.Class{
		"2026-04-21": {bookedClass, otherClass},
		"2026-04-22": {waitlistClass},
	}

	windowStart, _ := time.ParseInLocation("2006-01-02", "2026-04-21", loc)

	var b strings.Builder
	writeUserBookingsSection(&b, allBy, loc, windowStart, 3,
		"Your Arbox bookings:", "no note")

	out := b.String()

	if !strings.Contains(out, "WAITLIST 3/7") {
		t.Errorf("expected 'WAITLIST 3/7' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "BOOKED") {
		t.Errorf("expected 'BOOKED' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "CrossFit Hall A") {
		t.Errorf("expected waitlist class category in output, got:\n%s", out)
	}
	if !strings.Contains(out, "CrossFit Hall B") {
		t.Errorf("expected booked class category in output, got:\n%s", out)
	}
	if strings.Contains(out, "Open Box") {
		t.Errorf("unrelated class must not appear; got:\n%s", out)
	}
	if !strings.Contains(out, "schedule_id 100") || !strings.Contains(out, "schedule_id 200") {
		t.Errorf("expected schedule_ids to be surfaced, got:\n%s", out)
	}

	bookedIdx := strings.Index(out, "BOOKED")
	waitIdx := strings.Index(out, "WAITLIST")
	if bookedIdx < 0 || waitIdx < 0 || bookedIdx > waitIdx {
		t.Errorf("expected BOOKED (Apr 21) to appear before WAITLIST (Apr 22); got:\n%s", out)
	}

	// Weekday prefix must appear exactly once per line, not twice.
	// ("Mon Mon 20 Apr 08:30" was a pre-existing cosmetic bug because
	// time.Format("Mon 02 Jan") already prepends the abbreviated weekday.)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "· ") {
			continue
		}
		for _, wd := range []string{"Mon ", "Tue ", "Wed ", "Thu ", "Fri ", "Sat ", "Sun "} {
			if strings.Count(line, wd) > 1 {
				t.Errorf("weekday %q duplicated in line: %q", wd, line)
			}
		}
	}
}

// TestWriteUserBookingsSection_EmptyShowsNote verifies the empty path renders
// the fallback note so /status users who have no bookings get a clear message
// instead of just a title with nothing under it.
func TestWriteUserBookingsSection_EmptyShowsNote(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	windowStart, _ := time.ParseInLocation("2006-01-02", "2026-04-21", loc)

	var b strings.Builder
	writeUserBookingsSection(&b, nil, loc, windowStart, 3,
		"Bookings:", "check Arbox directly if you expected something here.")

	out := b.String()
	if !strings.Contains(out, "none in this window") {
		t.Errorf("expected empty-case phrase 'none in this window', got:\n%s", out)
	}
	if !strings.Contains(out, "check Arbox directly") {
		t.Errorf("expected empty-case note text, got:\n%s", out)
	}
}
