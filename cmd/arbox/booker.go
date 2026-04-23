package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/notify"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

// bookingResult is the terminal status of one auto-book attempt.
type bookingResult string

const (
	resultBooked     bookingResult = "BOOKED"
	resultWaitlisted bookingResult = "WAITLIST"
	resultFailed     bookingResult = "FAILED"
)

// bookingAttempt is one persisted record per `schedule_id` so the daemon does
// not retry the same class forever every tick.
//
// Terminal + TerminalUntil exist to stop the per-minute retry storm that hits
// when Arbox rejects a booking for a reason retry can't fix — e.g. the class
// requires a medical waiver the user hasn't signed yet (HTTP 514 with
// `messageToUser.name: medicalWavierRestricted`). Without these fields, the
// safety-net booker tries again on the next tick and floods Telegram with
// identical "booking failed" messages.
//
// A terminal failure still expires (default 6 hours via TerminalBackoff) so
// the daemon tries once more after the user has had time to sign the form /
// change their plan / etc. 6 h is a compromise between "re-notify promptly
// when things change" and "don't spam if the user is asleep".
type bookingAttempt struct {
	ScheduleID int           `json:"schedule_id"`
	Result     bookingResult `json:"result"`
	Message    string        `json:"message,omitempty"`
	When       time.Time     `json:"when"`
	HTTPStatus int           `json:"http_status,omitempty"`
	Slot       string        `json:"slot,omitempty"` // YYYY-MM-DD HH:MM Category
	// Terminal flags a failure that will not change on retry (see type doc).
	Terminal bool `json:"terminal,omitempty"`
	// TerminalUntil is when we'll let the booker try this schedule_id again
	// after a terminal failure. Zero when Terminal is false.
	TerminalUntil time.Time `json:"terminal_until,omitempty"`
}

// TerminalBackoff is how long we consider a terminal failure "fresh" — for
// this long after the failure we skip the class on every tick. After that
// we let the booker try once more, which either succeeds (user fixed the
// underlying issue) or records a new terminal attempt with a fresh window.
const TerminalBackoff = 6 * time.Hour

// isTerminalHTTP returns true when the response status unambiguously means
// "retrying won't help without user action" — currently HTTP 514, which
// Arbox uses to signal business-rule failures (medicalWavierRestricted,
// planNotAllowed, scheduleFullForMember, etc. — all surfaced with a
// `messageToUser` the API consumer is expected to show the member). Other
// 4xx / 5xx stay retriable because they often ARE transient (Cloudflare
// hiccup, rate-limit, stale token the client can re-login).
func isTerminalHTTP(status int) bool {
	return status == 514
}

// attemptsState is the on-disk shape: map keyed by schedule_id.
type attemptsState struct {
	Attempts map[int]bookingAttempt `json:"attempts"`
}

func readAttemptsState() attemptsState {
	s := attemptsState{Attempts: map[int]bookingAttempt{}}
	b, err := os.ReadFile(bookingAttemptsPath())
	if err != nil {
		return s
	}
	if len(b) == 0 {
		return s
	}
	_ = json.Unmarshal(b, &s)
	if s.Attempts == nil {
		s.Attempts = map[int]bookingAttempt{}
	}
	return s
}

func writeAttemptsState(s attemptsState) error {
	path := bookingAttemptsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pruneAttempts drops entries whose class start is in the past so the file
// doesn't grow forever. Caller passes "now" so tests stay deterministic.
func pruneAttempts(s *attemptsState, now time.Time) {
	for id, a := range s.Attempts {
		if a.When.Before(now.Add(-30 * 24 * time.Hour)) {
			delete(s.Attempts, id)
		}
	}
}

// bookSlot is one (date, time) the daemon may book. Exactly one option per
// slot now that priority fallback is removed; Options stays a slice to keep
// the function signature stable but will have len==1 in practice.
type bookSlot struct {
	ClassStart time.Time
	Options    []schedule.PlannedOption
}

// groupOptionsBySlot groups a flat option list by ClassStart, sorted by start.
// In the current one-option-per-day world each ClassStart only has a single
// PlannedOption, so the inner group is trivially len==1; keeping the grouping
// shape means downstream callers that used to iterate Options still compile.
func groupOptionsBySlot(opts []schedule.PlannedOption) []bookSlot {
	byStart := map[time.Time][]schedule.PlannedOption{}
	var keys []time.Time
	for _, o := range opts {
		if _, ok := byStart[o.ClassStart]; !ok {
			keys = append(keys, o.ClassStart)
		}
		byStart[o.ClassStart] = append(byStart[o.ClassStart], o)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	out := make([]bookSlot, 0, len(keys))
	for _, k := range keys {
		out = append(out, bookSlot{ClassStart: k, Options: byStart[k]})
	}
	return out
}

// alreadyHoldsAtStart returns true if `you` is BOOKED or WAITLIST on any
// class with this exact start in the day's class list. Prevents duplicate
// booking when the user manually booked or when a higher-priority option is
// already held.
func alreadyHoldsAtStart(classes []arboxapi.Class, start time.Time, loc *time.Location, dayKey string) bool {
	target := start.Format("15:04")
	for _, cl := range classes {
		if cl.YouStatus() == "" {
			continue
		}
		if hhmm(cl.Time) == target {
			return true
		}
		// Also compare by parsed time in loc to be safe with formats.
		if when, err := classStartsAt(cl, dayKey, loc); err == nil {
			if when.Equal(start) {
				return true
			}
		}
	}
	return false
}

// runBooker tries to book every PlannedOption whose booking window has opened
// but whose class hasn't started yet. It respects a persisted attempts file so
// each schedule_id is only acted on once.
//
// Concurrency note: the proactive scheduler holds bookerMu while calling this
// and the 5-min ticker calls it under the same lock — the two paths can never
// fire BookClass for the same id simultaneously.
//
// Returns a short multi-line summary suitable for the daily heartbeat.
func runBooker(
	ctx context.Context,
	cfg *config.Config,
	client *arboxapi.Client,
	notifier notify.Notifier,
	locID, days int,
	now time.Time,
) (string, error) {
	loc := cfg.Location()
	opts, err := schedule.NextOptions(cfg, now, days)
	if err != nil {
		return "", fmt.Errorf("resolve options: %w", err)
	}
	if len(opts) == 0 {
		return "", nil
	}

	state := readAttemptsState()
	pruneAttempts(&state, now)

	// Membership id is needed for every book/standby call. Resolve once.
	var membershipID int
	resolveMember := func() (int, error) {
		if membershipID > 0 {
			return membershipID, nil
		}
		mid, err := ensureMembershipUserID(ctx, client)
		if err != nil {
			return 0, err
		}
		membershipID = mid
		return mid, nil
	}

	// Pre-fetch each requested calendar day once.
	dayCache := map[string][]arboxapi.Class{}
	getDay := func(dayKey string, anchor time.Time) ([]arboxapi.Class, error) {
		if v, ok := dayCache[dayKey]; ok {
			return v, nil
		}
		fctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		cls, err := client.GetScheduleDay(fctx, anchor, locID)
		if err != nil {
			return nil, err
		}
		dayCache[dayKey] = cls
		return cls, nil
	}

	var summaryLines []string
	stateDirty := false

	for _, slot := range groupOptionsBySlot(opts) {
		if !slot.ClassStart.After(now) {
			continue // class already started
		}
		// Window must already be open. (slot has ≥1 option; pick its WindowOpen.)
		windowOpen := slot.Options[0].WindowOpen
		if windowOpen.After(now) {
			continue // not time yet
		}

		dayKey := slot.ClassStart.Format("2006-01-02")
		dayClasses, err := getDay(dayKey, slot.ClassStart)
		if err != nil {
			line := fmt.Sprintf("%s %s — fetch failed: %v",
				dayKey, slot.ClassStart.Format("15:04"), err)
			summaryLines = append(summaryLines, line)
			continue
		}

		// If the user already has a class at this exact start, skip.
		if alreadyHoldsAtStart(dayClasses, slot.ClassStart, loc, dayKey) {
			summaryLines = append(summaryLines,
				fmt.Sprintf("%s %s — already held", dayKey, slot.ClassStart.Format("15:04")))
			continue
		}

		// Try options in priority order. Stop after the first terminal
		// outcome (success, waitlisted, or terminal failure).
		acted := false
		for _, opt := range slot.Options {
			matches := resolveOption(opt, dayClasses, cfg.CategoryFilter)
			if len(matches) == 0 {
				continue
			}
			cl := matches[0]
			if cl.YouStatus() != "" {
				summaryLines = append(summaryLines,
					fmt.Sprintf("%s %s %s (id %d) — already %s",
						dayKey, hhmm(cl.Time), cl.ResolvedCategoryName(), cl.ID, cl.YouStatus()))
				acted = true
				break
			}
			if prev, ok := state.Attempts[cl.ID]; ok {
				// Skip if we already succeeded (BOOKED/WAITLIST) — never retry
				// a won slot. Also skip terminal failures within their backoff
				// window so a medicalWavierRestricted / planNotAllowed / etc.
				// doesn't spam Telegram every 60 s.
				skip := prev.Result != resultFailed ||
					(prev.Terminal && now.Before(prev.TerminalUntil))
				if skip {
					reason := string(prev.Result)
					if prev.Terminal {
						reason = fmt.Sprintf("%s (terminal, retry after %s)",
							prev.Result, prev.TerminalUntil.Format("15:04"))
					}
					summaryLines = append(summaryLines,
						fmt.Sprintf("%s %s %s (id %d) — prior %s, skipping",
							dayKey, hhmm(cl.Time), cl.ResolvedCategoryName(), cl.ID, reason))
					acted = true
					break
				}
			}

			mid, err := resolveMember()
			if err != nil {
				return "", fmt.Errorf("membership: %w", err)
			}

			slotLabel := fmt.Sprintf("%s %s %s",
				dayKey, hhmm(cl.Time), cl.ResolvedCategoryName())

			if cl.Free > 0 {
				attempt, line := tryBookOnce(ctx, client, mid, cl, slotLabel, now)
				state.Attempts[cl.ID] = attempt
				stateDirty = true
				summaryLines = append(summaryLines, line)
				notifyBookingResult(notifier, cl, attempt, slot.ClassStart)
				acted = (attempt.Result == resultBooked)
				if acted {
					break
				}
				// Booking failed (full / API error). Try waitlist as last resort.
				wl := tryWaitlistOnce(ctx, client, mid, cl, slotLabel, now)
				state.Attempts[cl.ID] = wl
				summaryLines = append(summaryLines,
					fmt.Sprintf("%s — waitlist %s", slotLabel, wl.Result))
				notifyBookingResult(notifier, cl, wl, slot.ClassStart)
				acted = wl.Result == resultWaitlisted
				if acted {
					break
				}
				// Try next priority option.
				continue
			}

			// Class is full: go straight to waitlist.
			wl := tryWaitlistOnce(ctx, client, mid, cl, slotLabel, now)
			state.Attempts[cl.ID] = wl
			stateDirty = true
			summaryLines = append(summaryLines,
				fmt.Sprintf("%s — full → waitlist %s", slotLabel, wl.Result))
			notifyBookingResult(notifier, cl, wl, slot.ClassStart)
			if wl.Result == resultWaitlisted {
				acted = true
				break
			}
			// Else try the next priority option.
		}

		if !acted {
			summaryLines = append(summaryLines,
				fmt.Sprintf("%s %s — no plan tier could be booked",
					dayKey, slot.ClassStart.Format("15:04")))
		}
	}

	if stateDirty {
		if err := writeAttemptsState(state); err != nil {
			fmt.Printf("[booker] persist attempts: %v\n", err)
		}
	}
	return strings.Join(summaryLines, "\n"), nil
}

func tryBookOnce(
	ctx context.Context,
	client *arboxapi.Client,
	membershipID int,
	cl arboxapi.Class,
	slotLabel string,
	now time.Time,
) (bookingAttempt, string) {
	bctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	res, err := client.BookClass(bctx, membershipID, cl.ID, false)
	att := bookingAttempt{ScheduleID: cl.ID, When: now, Slot: slotLabel}
	if res != nil {
		att.HTTPStatus = res.StatusCode
		att.Message = res.Message
	}
	if err == nil && res != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
		att.Result = resultBooked
		return att, fmt.Sprintf("%s — BOOKED (id %d)", slotLabel, cl.ID)
	}
	att.Result = resultFailed
	if err != nil && att.Message == "" {
		att.Message = err.Error()
	}
	if isTerminalHTTP(att.HTTPStatus) {
		att.Terminal = true
		att.TerminalUntil = now.Add(TerminalBackoff)
	}
	return att, fmt.Sprintf("%s — book FAILED (id %d): %s", slotLabel, cl.ID, att.Message)
}

func tryWaitlistOnce(
	ctx context.Context,
	client *arboxapi.Client,
	membershipID int,
	cl arboxapi.Class,
	slotLabel string,
	now time.Time,
) bookingAttempt {
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	res, err := client.JoinWaitlist(wctx, membershipID, cl.ID, false)
	att := bookingAttempt{ScheduleID: cl.ID, When: now, Slot: slotLabel}
	if res != nil {
		att.HTTPStatus = res.StatusCode
		att.Message = res.Message
	}
	if err == nil && res != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
		att.Result = resultWaitlisted
		return att
	}
	att.Result = resultFailed
	if err != nil && att.Message == "" {
		att.Message = err.Error()
	}
	if isTerminalHTTP(att.HTTPStatus) {
		att.Terminal = true
		att.TerminalUntil = now.Add(TerminalBackoff)
	}
	return att
}

func notifyBookingResult(n notify.Notifier, cl arboxapi.Class, att bookingAttempt, classStart time.Time) {
	if n == nil {
		return
	}
	var ev notify.Event
	switch att.Result {
	case resultBooked:
		ev = notify.EventBooked
	case resultWaitlisted:
		ev = notify.EventWaitlisted
	case resultFailed:
		ev = notify.EventFailed
	default:
		return
	}
	text := fmt.Sprintf("%s · id %d", cl.ResolvedCategoryName(), cl.ID)
	if att.Message != "" {
		text += " · " + att.Message
	}
	_ = n.Notify(notify.Message{
		Event:      ev,
		Text:       text,
		ClassStart: classStart,
	})
}
