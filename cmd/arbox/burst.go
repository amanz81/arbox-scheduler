package main

import (
	"context"
	"fmt"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/notify"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

// Burst tuning. The default duration matters because some popular gym slots
// sell out within seconds; retrying once a second for ~45s gives us a strong
// chance to land a spot if the API briefly returns "registration not yet
// open" or "concurrent contention" before settling.
const (
	burstDuration = 45 * time.Second
	burstInterval = 1 * time.Second
	// burstFetchEvery N attempts re-fetches the schedule. The fetch costs
	// ~250-600ms; if we re-fetch every attempt we slow the loop. Caching the
	// last fetch and re-fetching every 3rd attempt gives fresh `free` counts
	// without hurting reaction time.
	burstFetchEvery = 3
)

// bookSlotBurst aggressively retries the priority list for ONE specific
// ClassStart slot for up to burstDuration, sleeping burstInterval between
// attempts. Stops early on BOOKED, WAITLIST, already-held, or class start.
//
// All terminal outcomes are persisted to the attempts file and notified to
// Telegram via the standard notify events.
func bookSlotBurst(
	ctx context.Context,
	cfg *config.Config,
	client *arboxapi.Client,
	notifier notify.Notifier,
	locID int,
	targetStart time.Time,
) {
	loc := cfg.Location()
	dayKey := targetStart.Format("2006-01-02")

	deadline := time.Now().Add(burstDuration)
	if !deadline.Before(targetStart) {
		// Never burst past the actual class start.
		deadline = targetStart.Add(-1 * time.Second)
	}

	// Resolve which planned options correspond to this exact ClassStart.
	horizon := int(targetStart.Sub(time.Now()).Hours()/24) + 2
	if horizon < 1 {
		horizon = 1
	}
	opts, err := schedule.NextOptions(cfg, time.Now().In(loc), horizon)
	if err != nil {
		fmt.Printf("[burst] resolve options: %v\n", err)
		return
	}
	var slot *bookSlot
	for _, s := range groupOptionsBySlot(opts) {
		if s.ClassStart.Equal(targetStart) {
			s := s // capture
			slot = &s
			break
		}
	}
	if slot == nil {
		fmt.Printf("[burst] no plan options for %s\n", targetStart.Format("2006-01-02 15:04"))
		return
	}

	membershipID, err := ensureMembershipUserID(ctx, client)
	if err != nil {
		fmt.Printf("[burst] membership: %v\n", err)
		return
	}

	state := readAttemptsState()
	pruneAttempts(&state, time.Now())

	var (
		classes      []arboxapi.Class
		classesAt    time.Time
		attemptN     int
		lastFailMsg  string
	)

	for {
		attemptN++
		now := time.Now().In(loc)
		if !now.Before(deadline) {
			break
		}

		// Re-fetch schedule on first attempt and every Nth attempt; use cached
		// rows in between for fast iteration.
		if classes == nil || attemptN%burstFetchEvery == 1 {
			fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cls, ferr := client.GetScheduleDay(fctx, targetStart, locID)
			cancel()
			if ferr != nil {
				fmt.Printf("[burst #%d] fetch failed: %v\n", attemptN, ferr)
				if !sleepOrDone(ctx, burstInterval) {
					return
				}
				continue
			}
			classes = cls
			classesAt = time.Now()
		}

		if alreadyHoldsAtStart(classes, targetStart, loc, dayKey) {
			fmt.Printf("[burst] already held at %s — done\n", targetStart.Format("15:04"))
			return
		}

		// Try the priority list once per attempt.
		stop := false
		for _, opt := range slot.Options {
			matches := resolveOption(opt, classes, cfg.CategoryFilter)
			if len(matches) == 0 {
				continue
			}
			cl := matches[0]

			if cl.YouStatus() != "" {
				fmt.Printf("[burst] you=%s on id %d — done\n", cl.YouStatus(), cl.ID)
				return
			}

			slotLabel := fmt.Sprintf("%s %s %s",
				dayKey, hhmm(cl.Time), cl.ResolvedCategoryName())

			// If the cached snapshot says full, skip ahead to waitlist; if it
			// says spots free, try BookClass first.
			if cl.Free > 0 {
				att, line := tryBookOnce(ctx, client, membershipID, cl, slotLabel, time.Now())
				state.Attempts[cl.ID] = att
				fmt.Printf("[burst #%d] %s\n", attemptN, line)
				if att.Result == resultBooked {
					notifyBookingResult(notifier, cl, att, targetStart)
					_ = writeAttemptsState(state)
					return
				}
				lastFailMsg = att.Message
				// Force a fresh fetch next iteration so we don't loop on a
				// stale "free > 0" view.
				classes = nil
			}

			if cl.Free == 0 {
				wl := tryWaitlistOnce(ctx, client, membershipID, cl, slotLabel, time.Now())
				state.Attempts[cl.ID] = wl
				fmt.Printf("[burst #%d] waitlist %s id %d: %s\n",
					attemptN, wl.Result, cl.ID, wl.Message)
				if wl.Result == resultWaitlisted {
					notifyBookingResult(notifier, cl, wl, targetStart)
					_ = writeAttemptsState(state)
					return
				}
				lastFailMsg = wl.Message
			}
			// move to next priority option this attempt
		}

		if stop {
			break
		}
		// Pace the burst.
		if !sleepOrDone(ctx, burstInterval) {
			return
		}
		_ = classesAt // silence unused warning for the cache timestamp (debug only)
	}

	// Burst exhausted without success — persist whatever we recorded and tell
	// Telegram so the user knows the daemon DID try, vs. silent failure.
	_ = writeAttemptsState(state)
	if lastFailMsg == "" {
		lastFailMsg = "no plan tier could be booked"
	}
	_ = notifier.Notify(notify.Message{
		Event:      notify.EventFailed,
		Text:       fmt.Sprintf("burst exhausted after %d attempts · %s", attemptN, lastFailMsg),
		ClassStart: targetStart,
	})
	fmt.Printf("[burst] exhausted %d attempts in %s — %s\n",
		attemptN, burstDuration, targetStart.Format("2006-01-02 15:04"))
}
