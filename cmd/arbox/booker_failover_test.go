package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

// Hall failover (added 2026-11-25): when the planned CrossFit hall has no
// class at the planned time — or its class is full — the booker tries the
// other hall at the same time before falling back to a waitlist. These
// tests drive runBooker against a fake Arbox server and assert exactly
// which mutations were sent.

// fakeArbox records book/waitlist calls and serves a fixed class list.
type fakeArbox struct {
	mu          sync.Mutex
	classes     []map[string]any
	bookedIDs   []int
	waitlistIDs []int
}

func (f *fakeArbox) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/schedule/betweenDates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": f.classes})
	})
	record := func(dst *[]int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			*dst = append(*dst, int(body["schedule_id"].(float64)))
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}
	mux.HandleFunc("/api/v2/scheduleUser/insert", record(&f.bookedIDs))
	mux.HandleFunc("/api/v2/scheduleStandBy/insert", record(&f.waitlistIDs))
	return mux
}

func fakeClass(id int, date, tm, name string, free, maxUsers int) map[string]any {
	return map[string]any{
		"id":             id,
		"date":           date,
		"time":           tm,
		"end_time":       "09:30",
		"max_users":      maxUsers,
		"registered":     maxUsers - free,
		"free":           free,
		"stand_by":       0,
		"box_categories": map[string]any{"id": 1, "name": name},
	}
}

func runBookerAgainstFake(t *testing.T, cfg *config.Config, fake *fakeArbox, now time.Time) string {
	t.Helper()
	t.Setenv("ARBOX_MEMBERSHIP_USER_ID", "4242")
	t.Setenv("ARBOX_BOOKING_ATTEMPTS", filepath.Join(t.TempDir(), "booking_attempts.json"))
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	client := &arboxapi.Client{BaseURL: srv.URL, HTTP: srv.Client()}
	summary, err := runBooker(context.Background(), cfg, client, nil, 1575, 7, now)
	if err != nil {
		t.Fatalf("runBooker: %v", err)
	}
	return summary
}

func thuPlanCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Timezone: "Asia/Jerusalem",
		Days: map[string]config.DayConfig{
			"thursday": {Enabled: true, Options: []config.ClassOption{
				{Time: "08:30", Category: "Crossfit Hall B"},
			}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cfg
}

// Thu 2026-11-26 08:30 slot; booking window opens Wed 2026-11-25 08:30.
var thuNow = time.Date(2026, 11, 25, 8, 30, 0, 0, mustLoc("Asia/Jerusalem"))

func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func TestFailover_PreferredAvailable_BooksPreferredOnly(t *testing.T) {
	fake := &fakeArbox{classes: []map[string]any{
		fakeClass(101, "2026-11-26", "08:30", "Crossfit Hall B", 5, 15),
		fakeClass(102, "2026-11-26", "08:30", "CrossFit- Hall A", 5, 18),
	}}
	runBookerAgainstFake(t, thuPlanCfg(t), fake, thuNow)
	if len(fake.bookedIDs) != 1 || fake.bookedIDs[0] != 101 {
		t.Fatalf("booked = %v, want [101]", fake.bookedIDs)
	}
	if len(fake.waitlistIDs) != 0 {
		t.Fatalf("waitlist = %v, want none", fake.waitlistIDs)
	}
}

func TestFailover_PreferredMissing_BooksOtherHall(t *testing.T) {
	// The 2026-11-26 incident shape, except Hall A has spots here:
	// no Hall B class exists at all on Thursday.
	fake := &fakeArbox{classes: []map[string]any{
		fakeClass(102, "2026-11-26", "08:30", "CrossFit- Hall A", 3, 18),
	}}
	runBookerAgainstFake(t, thuPlanCfg(t), fake, thuNow)
	if len(fake.bookedIDs) != 1 || fake.bookedIDs[0] != 102 {
		t.Fatalf("booked = %v, want [102]", fake.bookedIDs)
	}
	if len(fake.waitlistIDs) != 0 {
		t.Fatalf("waitlist = %v, want none", fake.waitlistIDs)
	}
}

func TestFailover_PreferredFull_BooksOtherHallBeforeWaitlist(t *testing.T) {
	fake := &fakeArbox{classes: []map[string]any{
		fakeClass(101, "2026-11-26", "08:30", "Crossfit Hall B", 0, 15),
		fakeClass(102, "2026-11-26", "08:30", "CrossFit- Hall A", 2, 18),
	}}
	runBookerAgainstFake(t, thuPlanCfg(t), fake, thuNow)
	if len(fake.bookedIDs) != 1 || fake.bookedIDs[0] != 102 {
		t.Fatalf("booked = %v, want [102]", fake.bookedIDs)
	}
	if len(fake.waitlistIDs) != 0 {
		t.Fatalf("waitlist = %v, want none (a real spot beats a waitlist)", fake.waitlistIDs)
	}
}

func TestFailover_BothFull_WaitlistsPreferred(t *testing.T) {
	fake := &fakeArbox{classes: []map[string]any{
		fakeClass(101, "2026-11-26", "08:30", "Crossfit Hall B", 0, 15),
		fakeClass(102, "2026-11-26", "08:30", "CrossFit- Hall A", 0, 18),
	}}
	runBookerAgainstFake(t, thuPlanCfg(t), fake, thuNow)
	if len(fake.bookedIDs) != 0 {
		t.Fatalf("booked = %v, want none", fake.bookedIDs)
	}
	if len(fake.waitlistIDs) != 1 || fake.waitlistIDs[0] != 101 {
		t.Fatalf("waitlist = %v, want [101] (preferred hall)", fake.waitlistIDs)
	}
}

func TestFailover_PreferredMissingOtherFull_WaitlistsOther(t *testing.T) {
	fake := &fakeArbox{classes: []map[string]any{
		fakeClass(102, "2026-11-26", "08:30", "CrossFit- Hall A", 0, 18),
	}}
	runBookerAgainstFake(t, thuPlanCfg(t), fake, thuNow)
	if len(fake.bookedIDs) != 0 {
		t.Fatalf("booked = %v, want none", fake.bookedIDs)
	}
	if len(fake.waitlistIDs) != 1 || fake.waitlistIDs[0] != 102 {
		t.Fatalf("waitlist = %v, want [102]", fake.waitlistIDs)
	}
}

func TestFailover_Weightlifting_NoSwap(t *testing.T) {
	cfg := &config.Config{
		Timezone: "Asia/Jerusalem",
		Days: map[string]config.DayConfig{
			"tuesday": {Enabled: true, Options: []config.ClassOption{
				{Time: "09:00", Category: "Weightlifting Hall B"},
			}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Tuesday has no Weightlifting class, only a CrossFit Hall A class at
	// the same time. Weightlifting has no counterpart hall: no swap.
	fake := &fakeArbox{classes: []map[string]any{
		fakeClass(201, "2026-11-24", "09:00", "CrossFit- Hall A", 5, 18),
	}}
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, mustLoc("Asia/Jerusalem")) // Mon, window open
	summary := runBookerAgainstFake(t, cfg, fake, now)
	if len(fake.bookedIDs) != 0 || len(fake.waitlistIDs) != 0 {
		t.Fatalf("booked=%v waitlist=%v, want no mutations for Weightlifting", fake.bookedIDs, fake.waitlistIDs)
	}
	if !strings.Contains(summary, "no plan tier could be booked") {
		t.Fatalf("summary = %q, want the no-tier line", summary)
	}
}

func TestFailover_AlreadyWaitlistedAtStart_NoDuplicateAttempt(t *testing.T) {
	// Tomorrow's real shape (Thu 2026-11-26): no Hall B class, Hall A is
	// full, and the user already holds the Hall A waitlist spot. The
	// booker must not attempt any duplicate booking or waitlist join.
	held := fakeClass(102, "2026-11-26", "08:30", "CrossFit- Hall A", 0, 18)
	held["user_in_standby"] = 777
	held["stand_by"] = 1
	held["stand_by_position"] = 1
	fake := &fakeArbox{classes: []map[string]any{held}}
	summary := runBookerAgainstFake(t, thuPlanCfg(t), fake, thuNow)
	if len(fake.bookedIDs) != 0 || len(fake.waitlistIDs) != 0 {
		t.Fatalf("booked=%v waitlist=%v, want no mutations (already held)", fake.bookedIDs, fake.waitlistIDs)
	}
	if !strings.Contains(summary, "already held") {
		t.Fatalf("summary = %q, want 'already held'", summary)
	}
}

// --- unit tests for the candidate selection itself -------------------------

func mkClass(id int, tm, name string, free int) arboxapi.Class {
	c := arboxapi.Class{ID: id, Date: "2026-11-26", Time: tm, Free: free, MaxUsers: 18}
	c.BoxCategories.Name = name
	return c
}

func TestSlotCandidateClasses_OrderAndScope(t *testing.T) {
	flt := config.CategoryFilter{}
	day := []arboxapi.Class{
		mkClass(102, "08:30", "CrossFit- Hall A", 3),
		mkClass(101, "08:30", "Crossfit Hall B", 5),
		mkClass(103, "09:30", "Crossfit Hall B", 5),
	}
	opt := schedule.PlannedOption{Time: "08:30", Category: "Crossfit Hall B"}
	got := slotCandidateClasses(opt, day, flt)
	if len(got) != 2 || got[0].ID != 101 || got[1].ID != 102 {
		t.Fatalf("candidates = %+v, want [101 102]", got)
	}

	// Reverse direction: plan Hall A, failover to Hall B.
	optA := schedule.PlannedOption{Time: "08:30", Category: "CrossFit- Hall A"}
	got = slotCandidateClasses(optA, day, flt)
	if len(got) != 2 || got[0].ID != 102 || got[1].ID != 101 {
		t.Fatalf("candidates = %+v, want [102 101]", got)
	}

	// Weightlifting: no failover even with a CrossFit class at the time.
	optW := schedule.PlannedOption{Time: "08:30", Category: "Weightlifting Hall B"}
	if got := slotCandidateClasses(optW, day, flt); len(got) != 0 {
		t.Fatalf("weightlifting candidates = %+v, want none", got)
	}
}

func TestSlotCandidateClasses_RespectsGlobalExclude(t *testing.T) {
	flt := config.CategoryFilter{Exclude: []string{"Hall A"}}
	day := []arboxapi.Class{
		mkClass(102, "08:30", "CrossFit- Hall A", 3),
	}
	opt := schedule.PlannedOption{Time: "08:30", Category: "Crossfit Hall B"}
	if got := slotCandidateClasses(opt, day, flt); len(got) != 0 {
		t.Fatalf("candidates = %+v, want none (Hall A excluded)", got)
	}
}
