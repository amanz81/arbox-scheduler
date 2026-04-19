package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/notify"
)

type captureNotifier struct {
	events []notify.Message
}

func (c *captureNotifier) Notify(m notify.Message) error {
	c.events = append(c.events, m)
	return nil
}

func TestSlotsAtWindow_groupsBySharedWindow(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	cfg := &config.Config{
		Timezone:    "Asia/Jerusalem",
		DefaultTime: "08:30",
		Days: map[string]config.DayConfig{
			"sunday": {Enabled: true, Options: []config.ClassOption{
				{Time: "08:00", Category: "Hall A"},
				{Time: "08:00", Category: "Hall B"},
			}},
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
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, loc) // Friday noon
	// Sunday 08:00 - 48h = Friday 08:00 (already past); skip → next Sunday 08:00 - 48h = next Friday.
	// Use a window 7 days later so we have at least one upcoming Sunday.
	target := time.Date(2026, 4, 24, 8, 0, 0, 0, loc) // following Friday 08:00 = Sun 26 Apr 08:00 - 48h
	got := slotsAtWindow(cfg, now, 14, target)
	if len(got) != 1 {
		t.Fatalf("want 1 distinct ClassStart, got %d (%v)", len(got), got)
	}
	if got[0].Hour() != 8 || got[0].Minute() != 0 {
		t.Fatalf("expected 08:00 ClassStart, got %v", got[0])
	}
}

// TestBookSlotBurst_succeedsOnFirstAttempt drives bookSlotBurst against a
// fake Arbox server that returns one Sunday class with one free spot. The
// first BookClass call returns 200; we expect a single EventBooked notify
// and a persisted attempt.
func TestBookSlotBurst_succeedsOnFirstAttempt(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	// Pick the next Sunday at least 9 days out so windowOpen (Sun-48h) is
	// still in the future no matter when the test runs.
	now := time.Now().In(loc)
	target := nextSundayAfter(now.Add(9*24*time.Hour), 8, loc)

	dateStr := target.Format("2006-01-02")

	var bookCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v2/schedule/betweenDates"):
			body := map[string]any{"data": []map[string]any{{
				"id":         42,
				"date":       dateStr,
				"time":       "08:00",
				"max_users":  18,
				"registered": 17,
				"free":       1,
				"box_categories": map[string]any{"id": 1, "name": "CrossFit- Hall A"},
			}}}
			_ = json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/api/v2/scheduleUser/insert"):
			bookCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"id":777}}`))
		case strings.HasSuffix(r.URL.Path, "/api/v2/boxes/locations"):
			_, _ = w.Write([]byte(`{"data":[{"id":1130,"name":"Rose Valley CrossFit","locations_box":[{"id":1575,"name":""}]}]}`))
		case strings.Contains(r.URL.Path, "/api/v2/boxes/") && strings.HasSuffix(r.URL.Path, "/memberships/1"):
			_, _ = w.Write([]byte(`{"data":[{"id":99,"user_fk":1,"box_fk":1130,"active":1,"membership_types":{"name":"plan"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("ARBOX_BOX_ID", "1130")
	t.Setenv("ARBOX_LOCATIONS_BOX_ID", "1575")
	t.Setenv("ARBOX_MEMBERSHIP_USER_ID", "99")
	dir := t.TempDir()
	t.Setenv("ARBOX_BOOKING_ATTEMPTS", dir+"/booking_attempts.json")
	t.Setenv("ARBOX_ENV_FILE", dir+"/.env")

	client := arboxapi.New(srv.URL)
	client.Token = "tok"

	cfg := &config.Config{
		Timezone:    "Asia/Jerusalem",
		DefaultTime: "08:30",
		Days: map[string]config.DayConfig{
			"sunday": {Enabled: true, Options: []config.ClassOption{
				{Time: "08:00", Category: "Hall A"},
			}},
			"monday":    {Enabled: false},
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

	cap := &captureNotifier{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bookSlotBurst(ctx, cfg, client, cap, 1575, target)

	if got := bookCalls.Load(); got != 1 {
		t.Fatalf("want 1 BookClass call, got %d", got)
	}
	var booked bool
	for _, ev := range cap.events {
		if ev.Event == notify.EventBooked {
			booked = true
		}
	}
	if !booked {
		t.Fatalf("expected EventBooked notification, got %+v", cap.events)
	}
	state := readAttemptsState()
	if state.Attempts[42].Result != resultBooked {
		t.Fatalf("expected attempts[42]=BOOKED, got %+v", state.Attempts[42])
	}
}

func nextSundayAfter(t time.Time, hour int, loc *time.Location) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 14; i++ {
		c := d.AddDate(0, 0, i)
		if c.Weekday() == time.Sunday {
			return time.Date(c.Year(), c.Month(), c.Day(), hour, 0, 0, 0, loc)
		}
	}
	return t
}
