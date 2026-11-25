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

// failoverHallTokens returns the preferred hall token and its counterpart
// ("hall a" / "hall b") for a planned option's category, or ("", "") when
// hall failover does not apply. Failover is scoped to CrossFit hall slots:
// the category must name a CrossFit class in a specific hall. That keeps
// e.g. "Weightlifting Hall B" out — it has no counterpart hall to swap to.
func failoverHallTokens(category string) (mine, other string) {
	cat := strings.ToLower(strings.TrimSpace(category))
	if !strings.Contains(cat, "crossfit") {
		return "", ""
	}
	switch {
	case strings.Contains(cat, "hall a"):
		return "hall a", "hall b"
	case strings.Contains(cat, "hall b"):
		return "hall b", "hall a"
	}
	return "", ""
}

// failoverClass finds the OTHER CrossFit hall's class at the option's time
// in the day's class list: a CrossFit class whose category carries the
// opposite hall token (and not the preferred one). The counterpart is
// derived from the actual classes on offer rather than by rewriting the
// configured category string, so gym-side spelling differences ("CrossFit-
// Hall A" vs "Crossfit Hall B") don't matter. Global excludes are honored
// exactly as resolveOption honors them. Returns nil when failover does not
// apply or no such class exists.
func failoverClass(opt schedule.PlannedOption, day []arboxapi.Class, flt config.CategoryFilter) *arboxapi.Class {
	mine, other := failoverHallTokens(opt.Category)
	if other == "" {
		return nil
	}
	wantTime := hhmm(opt.Time)
	for i := range day {
		c := day[i]
		if hhmm(c.Time) != wantTime {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(c.ResolvedCategoryName()))
		if !strings.Contains(name, "crossfit") ||
			!strings.Contains(name, other) ||
			strings.Contains(name, mine) {
			continue
		}
		skip := false
		for _, ex := range flt.Exclude {
			if ex != "" && strings.Contains(name, strings.ToLower(ex)) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		return &day[i]
	}
	return nil
}

// slotCandidateClasses returns the ordered classes runBooker should act on
// for one planned option: the preferred class first (resolveOption's first
// match), then the other CrossFit hall's class at the same time as a
// failover candidate. Either may be absent; the failover entry is dropped
// when it resolves to the same class as the preferred one.
func slotCandidateClasses(opt schedule.PlannedOption, day []arboxapi.Class, flt config.CategoryFilter) []arboxapi.Class {
	var out []arboxapi.Class
	if matches := resolveOption(opt, day, flt); len(matches) > 0 {
		out = append(out, matches[0])
	}
	if fc := failoverClass(opt, day, flt); fc != nil {
		dup := false
		for _, c := range out {
			if c.ID == fc.ID {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, *fc)
		}
	}
	return out
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

		// Candidates in priority order: the planned class first, then the
		// other CrossFit hall's class at the same time (hall failover — see
		// slotCandidateClasses). Booking is attempted on every candidate
		// before any waitlist join, so a real spot in the other hall always
		// beats a waitlist spot in the preferred hall. If no candidate
		// books, we waitlist the first candidate that had a class at all
		// (the preferred class, or the other hall's when the preferred
		// class doesn't exist) so the user is never left with nothing.
		candidates := slotCandidateClasses(slot.Options[0], dayClasses, cfg.CategoryFilter)
		acted := false
		var waitlistTarget *arboxapi.Class
		var waitlistLabel string
		waitlistWasFull := false
		for _, cl := range candidates {
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
				if attempt.Result == resultBooked {
					acted = true
					break
				}
				// Booking failed (race filled it / API error). Remember
				// this class for the waitlist fallback below, but first let
				// any remaining candidate try to book a real spot.
				if waitlistTarget == nil {
					c := cl
					waitlistTarget = &c
					waitlistLabel = slotLabel
					waitlistWasFull = false
				}
				continue
			}

			// Class is full: remember it for the waitlist fallback and move
			// on — the other hall may still have a bookable spot.
			if waitlistTarget == nil {
				c := cl
				waitlistTarget = &c
				waitlistLabel = slotLabel
				waitlistWasFull = true
			}
		}

		// No candidate booked: join the waitlist of the first candidate
		// that had a class (preferred hall if it exists, else the other
		// hall) — the pre-failover behavior for a lone class.
		if !acted && waitlistTarget != nil {
			cl := *waitlistTarget
			mid, err := resolveMember()
			if err != nil {
				return "", fmt.Errorf("membership: %w", err)
			}
			wl := tryWaitlistOnce(ctx, client, mid, cl, waitlistLabel, now)
			state.Attempts[cl.ID] = wl
			stateDirty = true
			if waitlistWasFull {
				summaryLines = append(summaryLines,
					fmt.Sprintf("%s — full → waitlist %s", waitlistLabel, wl.Result))
			} else {
				summaryLines = append(summaryLines,
					fmt.Sprintf("%s — waitlist %s", waitlistLabel, wl.Result))
			}
			notifyBookingResult(notifier, cl, wl, slot.ClassStart)
			acted = wl.Result == resultWaitlisted
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
