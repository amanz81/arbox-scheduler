package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lafofo-nivo/arbox-scheduler/internal/arboxapi"
	"github.com/lafofo-nivo/arbox-scheduler/internal/config"
	"github.com/lafofo-nivo/arbox-scheduler/internal/notify"
	"github.com/lafofo-nivo/arbox-scheduler/internal/schedule"
)

// proactiveLead is how far in advance we wake up before WindowOpen so the
// access token is fresh, the schedule is pre-fetched, and we are ready to
// fire the booking the instant the window opens.
const proactiveLead = 8 * time.Second

// proactiveStrikeOffset fires the booking call this much *before* WindowOpen
// to compensate for client/server clock skew + network latency. If the
// daemon's clock is slightly behind Arbox's, hitting at WindowOpen-200ms
// usually still resolves on the API side at-or-after WindowOpen.
const proactiveStrikeOffset = 250 * time.Millisecond

// proactiveMaxSleep caps a single sleep so config changes (new /setup, new
// pause window) are picked up within an hour even when the next window is
// far in the future.
const proactiveMaxSleep = 1 * time.Hour

// bookerMu serializes runBooker calls so the 5-min ticker and the proactive
// goroutine can never double-fire BookClass on the same schedule_id.
var bookerMu sync.Mutex

// runProactiveBooker runs forever in a goroutine, sleeping until each
// upcoming PlannedOption's WindowOpen and then immediately invoking runBooker.
// It complements (not replaces) the 5-min tick which acts as a safety net.
func runProactiveBooker(
	ctx context.Context,
	cfgReload func() (*config.Config, error),
	client *arboxapi.Client,
	notifier notify.Notifier,
	locID, days int,
) {
	for {
		if ctx.Err() != nil {
			return
		}

		cfg, err := cfgReload()
		if err != nil {
			fmt.Printf("[proactive] reload config: %v\n", err)
			if !sleepOrDone(ctx, time.Minute) {
				return
			}
			continue
		}
		loc := cfg.Location()
		now := time.Now().In(loc)

		nextWin, err := nextActionableWindow(cfg, now, days)
		if err != nil {
			fmt.Printf("[proactive] resolve next window: %v\n", err)
			if !sleepOrDone(ctx, time.Minute) {
				return
			}
			continue
		}
		if nextWin.IsZero() {
			// No upcoming windows — re-check periodically (config might change).
			if !sleepOrDone(ctx, proactiveMaxSleep) {
				return
			}
			continue
		}

		// Long sleep first if the window is far away. Re-evaluate every hour
		// so /pause, /setup, or config edits change behavior promptly.
		until := time.Until(nextWin) - proactiveLead
		if until > proactiveMaxSleep {
			if !sleepOrDone(ctx, proactiveMaxSleep) {
				return
			}
			continue
		}
		if until > 0 {
			fmt.Printf("[proactive] next strike at %s (in %s)\n",
				nextWin.Format("2006-01-02 15:04:05.000 MST"),
				time.Until(nextWin).Round(time.Second))
			if !sleepOrDone(ctx, until) {
				return
			}
		}

		// Pre-warm: refresh config + pause + auth probe.
		cfg, err = cfgReload()
		if err != nil {
			fmt.Printf("[proactive] pre-warm reload config: %v\n", err)
			continue
		}
		loc = cfg.Location()
		ps, _ := readPauseState()
		if ps.IsActive(time.Now().In(loc)) {
			fmt.Printf("[proactive] paused; skipping window %s\n",
				nextWin.Format("2006-01-02 15:04:05 MST"))
			// Wait until just past the window so we don't loop on the same one.
			if !sleepOrDone(ctx, time.Until(nextWin)+5*time.Second) {
				return
			}
			continue
		}

		// Touch the schedule API so a 401 triggers the silent re-login *before*
		// the strike instead of inside the booking call.
		probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
		_, _ = client.GetScheduleDay(probeCtx, nextWin, locID)
		cancelProbe()

		// Precise wait until WindowOpen-strikeOffset.
		precise := time.Until(nextWin) - proactiveStrikeOffset
		if precise > 0 {
			if !sleepOrDone(ctx, precise) {
				return
			}
		}

		fmt.Printf("[proactive] STRIKE at %s (planned window %s)\n",
			time.Now().In(loc).Format("15:04:05.000 MST"),
			nextWin.Format("15:04:05.000 MST"))

		// Identify every ClassStart whose WindowOpen is exactly nextWin (a
		// single Sunday 08:00 slot has multiple priority options sharing the
		// same WindowOpen). Burst-attack only those slots, not unrelated ones.
		targets := slotsAtWindow(cfg, time.Now().In(loc), days, nextWin)
		bookerMu.Lock()
		for _, target := range targets {
			bookSlotBurst(ctx, cfg, client, notifier, locID, target)
		}
		bookerMu.Unlock()

		// Small breather so very close windows (back-to-back) don't tight-loop.
		if !sleepOrDone(ctx, 750*time.Millisecond) {
			return
		}
	}
}

// nextActionableWindow returns the earliest WindowOpen in the next `days`
// strictly after `now`, ignoring any schedule_id we already booked or
// waitlisted (best-effort: matched via attempts file by ClassStart key).
//
// It does NOT skip windows that have failed attempts — those are still worth
// retrying because spots may free up before class start.
func nextActionableWindow(cfg *config.Config, now time.Time, days int) (time.Time, error) {
	opts, err := schedule.NextOptions(cfg, now, days)
	if err != nil {
		return time.Time{}, err
	}
	if len(opts) == 0 {
		return time.Time{}, nil
	}
	for _, o := range opts {
		// Only consider windows that haven't opened yet.
		if !o.WindowOpen.After(now) {
			continue
		}
		return o.WindowOpen, nil
	}
	return time.Time{}, nil
}

// slotsAtWindow returns every distinct ClassStart whose WindowOpen equals
// (or is within 1s of) `target`. Multiple priority options share a single
// ClassStart and we want to burst that ClassStart only once.
func slotsAtWindow(cfg *config.Config, now time.Time, days int, target time.Time) []time.Time {
	opts, err := schedule.NextOptions(cfg, now, days)
	if err != nil {
		return nil
	}
	seen := map[time.Time]bool{}
	var out []time.Time
	for _, o := range opts {
		diff := o.WindowOpen.Sub(target)
		if diff < 0 {
			diff = -diff
		}
		if diff > time.Second {
			continue
		}
		if seen[o.ClassStart] {
			continue
		}
		seen[o.ClassStart] = true
		out = append(out, o.ClassStart)
	}
	return out
}

// sleepOrDone returns true if the sleep completed normally, false on context
// cancellation.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// asValueOrZero is unused outside this file but kept exported by name to
// silence accidental future references; it returns the value or its zero.
func asValueOrZero[T any](v T, ok bool) T {
	if !ok {
		var z T
		return z
	}
	return v
}

// classByID returns the class with the given id from `classes` (or false).
// Reserved for future enhancements (e.g. the proactive scheduler will fetch
// the schedule once and pass it directly to the booker for sub-second timing).
func classByID(classes []arboxapi.Class, id int) (arboxapi.Class, bool) {
	for _, c := range classes {
		if c.ID == id {
			return c, true
		}
	}
	return arboxapi.Class{}, false
}
