package main

// HTTP handler bodies for the LLM-facing API.
//
// Every handler is a method on *apiServer (so tests can build one with a
// fake arboxapi client). Handlers reuse the existing builders where they
// exist (runSelfTest, fetchScheduleWindow, parsePauseArgs, BookClass, etc.)
// — the API is a transport layer, NOT a parallel implementation.
//
// Mutating handlers follow this pattern:
//
//   1. Decode + validate the JSON body.
//   2. Parse common args (schedule_id etc.) for the audit line.
//   3. If !hasConfirm(r) → write a dry-run JSON, audit it, return 200.
//   4. Else → acquire bookerMu (for booking calls), invoke arboxapi, audit
//      the outcome, return the MutationResult as JSON.
//
// The audit log is written inside the handler (not in middleware) because
// only the handler knows the right `route`, `args`, and `result` values.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lafofo-nivo/arbox-scheduler/internal/arboxapi"
	"github.com/lafofo-nivo/arbox-scheduler/internal/config"
)

// ----- shared response shapes --------------------------------------------

type classResp struct {
	ScheduleID int    `json:"schedule_id"`
	Date       string `json:"date,omitempty"`
	Time       string `json:"time"`
	EndTime    string `json:"end_time,omitempty"`
	Category   string `json:"category"`
	Free       int    `json:"free"`
	Registered int    `json:"registered"`
	MaxUsers   int    `json:"max_users"`
	StandBy    int    `json:"stand_by"`
	YouStatus  string `json:"you_status"`
	// StandByPosition is the caller's 1-based ordinal on this class's waitlist
	// (1 = next in line). 0 when the user isn't on the waitlist OR when Arbox
	// didn't surface the field for this payload — treat 0 as "unknown" on the
	// consumer side. Kept alongside YouStatus + StandBy so an LLM can answer
	// "what is my position" without a second round-trip.
	StandByPosition int `json:"stand_by_position,omitempty"`
	// YouStatusDetail is the human-readable version of YouStatus that includes
	// the position when known — e.g. "WAITLIST 3/9" or "BOOKED". Empty when
	// the user has no relationship with the class.
	YouStatusDetail string `json:"you_status_detail,omitempty"`
	Coach           string `json:"coach,omitempty"`
}

func toClassResp(c arboxapi.Class) classResp {
	coach := ""
	if c.Coach != nil {
		coach = strings.TrimSpace(c.Coach.FullName)
	}
	return classResp{
		ScheduleID:      c.ID,
		Date:            c.Date,
		Time:            hhmm(c.Time),
		EndTime:         hhmm(c.EndTime),
		Category:        c.ResolvedCategoryName(),
		Free:            c.Free,
		Registered:      c.Registered,
		MaxUsers:        c.MaxUsers,
		StandBy:         c.StandBy,
		StandByPosition: c.StandByPosition,
		YouStatus:       c.YouStatus(),
		YouStatusDetail: c.YouStatusDetail(),
		Coach:           coach,
	}
}

// ----- /healthz ----------------------------------------------------------

func (s *apiServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	// The on-disk state path must be writable — that's the closest signal
	// to "this machine is healthy enough to act".
	path := bookingAttemptsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeError(w, http.StatusServiceUnavailable, "state dir not writable: "+err.Error(), "fs_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----- /version ----------------------------------------------------------

type pauseInfo struct {
	Active  bool       `json:"active"`
	Until   *time.Time `json:"until,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Summary string     `json:"summary,omitempty"`
}

type versionResp struct {
	Version         string    `json:"version"`
	Rev             string    `json:"rev"`
	Gym             string    `json:"gym"`
	TZ              string    `json:"tz"`
	LocationsBoxID  int       `json:"locations_box_id"`
	LookaheadDays   int       `json:"lookahead_days"`
	Pause           pauseInfo `json:"pause"`
}

func (s *apiServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	rev, modified := buildRevision()
	if modified {
		rev += "-dirty"
	}
	out := versionResp{
		Version:        Version,
		Rev:            rev,
		Gym:            cfg.Gym,
		TZ:             cfg.Timezone,
		LocationsBoxID: s.locID,
		LookaheadDays:  s.lookaheadDays,
	}
	if ps, err := readPauseState(); err == nil {
		loc := cfg.Location()
		now := time.Now().In(loc)
		if ps.IsActive(now) {
			until := ps.PausedUntil
			if loc != nil {
				until = until.In(loc)
			}
			out.Pause = pauseInfo{
				Active:  true,
				Until:   &until,
				Reason:  ps.Reason,
				Summary: ps.Summary(now, loc),
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ----- /selftest ---------------------------------------------------------

type selfCheckResp struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	LatencyMs int64  `json:"latency_ms"`
}

type selfTestResp struct {
	Checks       []selfCheckResp `json:"checks"`
	NextBookings []string        `json:"next_bookings"`
}

func (s *apiServer) handleSelfTest(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	days := queryIntDefault(r, "days", s.lookaheadDays)
	checks := runSelfTest(r.Context(), cfg, s.client, s.locID, days)
	out := selfTestResp{NextBookings: nextPlannedBookingsSummary(cfg, days, 5)}
	for _, c := range checks {
		out.Checks = append(out.Checks, selfCheckResp{
			Name: c.Name, OK: c.OK, Detail: c.Detail,
			LatencyMs: c.Latency.Milliseconds(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ----- /status -----------------------------------------------------------

type statusDayResp struct {
	Weekday  string      `json:"weekday"`
	Date     string      `json:"date,omitempty"`
	Enabled  bool        `json:"enabled"`
	Options  []planOpt   `json:"options,omitempty"`
	Live     []classResp `json:"live,omitempty"`
}

type planOpt struct {
	Time     string `json:"time"`
	Category string `json:"category,omitempty"`
}

type statusResp struct {
	Now      time.Time       `json:"now"`
	Timezone string          `json:"timezone"`
	Pause    pauseInfo       `json:"pause"`
	Days     []statusDayResp `json:"days"`
}

func (s *apiServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	days := queryIntDefault(r, "days", s.lookaheadDays)
	loc, now, windowStart, allBy, err := fetchScheduleWindow(r.Context(), cfg, s.client, s.locID, days)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "upstream")
		return
	}

	out := statusResp{Now: now, Timezone: cfg.Timezone}
	if ps, perr := readPauseState(); perr == nil && ps.IsActive(now) {
		until := ps.PausedUntil.In(loc)
		out.Pause = pauseInfo{
			Active: true, Until: &until,
			Reason: ps.Reason, Summary: ps.Summary(now, loc),
		}
	}
	for _, dk := range setupWeekdayOrder {
		d, ok := cfg.Days[dk]
		if !ok {
			continue
		}
		wd, okWD := dayKeyToWeekday[dk]
		if !okWD {
			continue
		}
		row := statusDayResp{Weekday: dk, Enabled: d.Enabled}
		key := nextOccurrenceKey(windowStart, wd, days)
		row.Date = key
		opts := cfg.OptionsFor(wd)
		for _, o := range opts {
			row.Options = append(row.Options, planOpt{Time: o.Time, Category: o.Category})
		}
		if key != "" {
			for _, c := range allBy[key] {
				if !classPassesGlobalFilter(c.ResolvedCategoryName(), cfg.CategoryFilter) {
					continue
				}
				row.Live = append(row.Live, toClassResp(c))
			}
		}
		out.Days = append(out.Days, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// ----- /classes ----------------------------------------------------------

func (s *apiServer) handleClasses(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	loc := cfg.Location()
	dateStr := r.URL.Query().Get("date")
	var day time.Time
	if dateStr == "" {
		now := time.Now().In(loc)
		day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		dateStr = day.Format("2006-01-02")
	} else {
		t, perr := time.ParseInLocation("2006-01-02", dateStr, loc)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "bad date: "+perr.Error(), "bad_request")
			return
		}
		day = t
	}
	filterOn := r.URL.Query().Get("filter") != "false"

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	classes, err := s.client.GetScheduleDay(ctx, day, s.locID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "upstream")
		return
	}
	out := struct {
		Date    string      `json:"date"`
		Classes []classResp `json:"classes"`
	}{Date: dateStr}
	for _, c := range classes {
		if filterOn && !classPassesGlobalFilter(c.ResolvedCategoryName(), cfg.CategoryFilter) {
			continue
		}
		out.Classes = append(out.Classes, toClassResp(c))
	}
	sort.SliceStable(out.Classes, func(i, j int) bool {
		return out.Classes[i].Time < out.Classes[j].Time
	})
	writeJSON(w, http.StatusOK, out)
}

// ----- /morning + /evening ----------------------------------------------

type windowDayResp struct {
	Date    string      `json:"date"`
	Weekday string      `json:"weekday"`
	Classes []classResp `json:"classes"`
}

type windowResp struct {
	Now      time.Time       `json:"now"`
	Timezone string          `json:"timezone"`
	From     int             `json:"from"`
	To       int             `json:"to"`
	Days     []windowDayResp `json:"days"`
}

func (s *apiServer) buildClassWindowJSON(ctx context.Context, cfg *config.Config, fromH, toH, days int) (windowResp, error) {
	loc, now, windowStart, allBy, err := fetchScheduleWindow(ctx, cfg, s.client, s.locID, days)
	if err != nil {
		return windowResp{}, err
	}
	out := windowResp{Now: now, Timezone: cfg.Timezone, From: fromH, To: toH}
	startMin := fromH * 60
	endMin := toH * 60
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		row := windowDayResp{Date: key, Weekday: strings.ToLower(d.Weekday().String())}
		for _, cl := range allBy[key] {
			if !classPassesGlobalFilter(cl.ResolvedCategoryName(), cfg.CategoryFilter) {
				continue
			}
			m, ok := parseHHMMMinutes(hhmm(cl.Time))
			if !ok || m < startMin || m >= endMin {
				continue
			}
			if i == 0 {
				when, werr := classStartsAt(cl, key, loc)
				if werr == nil && !when.After(now) {
					continue
				}
			}
			row.Classes = append(row.Classes, toClassResp(cl))
		}
		sort.SliceStable(row.Classes, func(i, j int) bool {
			return row.Classes[i].Time < row.Classes[j].Time
		})
		out.Days = append(out.Days, row)
	}
	return out, nil
}

func (s *apiServer) handleMorning(w http.ResponseWriter, r *http.Request) {
	s.handleWindow(w, r, 6, 12)
}

func (s *apiServer) handleEvening(w http.ResponseWriter, r *http.Request) {
	s.handleWindow(w, r, 16, 22)
}

func (s *apiServer) handleWindow(w http.ResponseWriter, r *http.Request, defFrom, defTo int) {
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	fromH := queryIntDefault(r, "from", defFrom)
	toH := queryIntDefault(r, "to", defTo)
	days := queryIntDefault(r, "days", 1)
	if fromH < 0 || fromH > 23 || toH < 1 || toH > 24 || fromH >= toH {
		writeError(w, http.StatusBadRequest, "invalid from/to (need 0<=from<to<=24)", "bad_request")
		return
	}
	if days < 1 || days > 30 {
		writeError(w, http.StatusBadRequest, "days out of range (1..30)", "bad_request")
		return
	}
	out, err := s.buildClassWindowJSON(r.Context(), cfg, fromH, toH, days)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "upstream")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ----- /bookings ---------------------------------------------------------

// bookingResp is one confirmed (BOOKED) or waitlisted (WAITLIST) entry in
// the caller's upcoming Arbox schedule. Fields beyond the minimal
// (schedule_id, when, category, status) exist so an LLM can answer
// follow-up questions — "what is my position", "how many free spots are
// there", "how full is the waitlist" — from a single bookings call.
type bookingResp struct {
	ScheduleID      int       `json:"schedule_id"`
	When            time.Time `json:"when"`
	Category        string    `json:"category"`
	Status          string    `json:"status"`         // "BOOKED" | "WAITLIST"
	StatusDetail    string    `json:"status_detail"`  // e.g. "WAITLIST 3/9"
	StandByPosition int       `json:"stand_by_position,omitempty"`
	StandByTotal    int       `json:"stand_by_total,omitempty"`
	Free            int       `json:"free"`
	Registered      int       `json:"registered"`
	MaxUsers        int       `json:"max_users"`
}

func (s *apiServer) handleBookings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	days := queryIntDefault(r, "days", 14)
	loc, now, windowStart, allBy, err := fetchScheduleWindow(r.Context(), cfg, s.client, s.locID, days)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "upstream")
		return
	}
	var out []bookingResp
	seen := map[int]bool{}
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		for _, c := range allBy[key] {
			st := c.YouStatus()
			if st == "" {
				continue
			}
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			when, werr := classStartsAt(c, key, loc)
			if werr != nil || when.Before(now) {
				continue
			}
			out = append(out, bookingResp{
				ScheduleID:      c.ID,
				When:            when,
				Category:        c.ResolvedCategoryName(),
				Status:          st,
				StatusDetail:    c.YouStatusDetail(),
				StandByPosition: c.StandByPosition,
				StandByTotal:    c.StandBy,
				Free:            c.Free,
				Registered:      c.Registered,
				MaxUsers:        c.MaxUsers,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].When.Before(out[j].When) })
	writeJSON(w, http.StatusOK, map[string]any{"bookings": out})
}

// ----- /plan -------------------------------------------------------------

func (s *apiServer) handlePlan(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// ----- /attempts ---------------------------------------------------------

func (s *apiServer) handleAttempts(w http.ResponseWriter, r *http.Request) {
	days := queryIntDefault(r, "days", 30)
	state := readAttemptsState()
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var out []bookingAttempt
	for _, a := range state.Attempts {
		if a.When.Before(cutoff) {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	writeJSON(w, http.StatusOK, map[string]any{"attempts": out})
}

// ----- /audit ------------------------------------------------------------

func (s *apiServer) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := queryIntDefault(r, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad since: "+err.Error(), "bad_request")
			return
		}
		since = t
	}
	entries, err := globalAuditLog.readTail(limit, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "audit_read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// ----- mutations ---------------------------------------------------------

// auditMutation writes one line to the audit log; errors are logged but
// don't fail the response.
func (s *apiServer) auditMutation(r *http.Request, route string, args map[string]any, confirm bool, scheduleID int, result string, latency time.Duration, httpStatus int, errMsg string) {
	e := auditEntry{
		TS:         nowAuditTS(),
		Route:      route,
		TokenKind:  string(tokenKindFromCtx(r.Context())),
		Args:       args,
		Confirm:    confirm,
		Result:     result,
		ScheduleID: scheduleID,
		LatencyMs:  latency.Milliseconds(),
		ClientIP:   clientIP(r),
		HTTPStatus: httpStatus,
		Error:      errMsg,
	}
	if err := globalAuditLog.appendOne(e); err != nil {
		fmt.Printf("[http] audit append: %v\n", err)
	}
}

func decodeJSONBody(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// --- /book ---

type bookReq struct {
	ScheduleID int `json:"schedule_id"`
}

func (s *apiServer) handleBook(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	var body bookReq
	if err := decodeJSONBody(r, &body); err != nil {
		s.auditMutation(r, "/api/v1/book", nil, false, 0, "", time.Since(t0), http.StatusBadRequest, err.Error())
		writeError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if body.ScheduleID <= 0 {
		s.auditMutation(r, "/api/v1/book", map[string]any{"schedule_id": body.ScheduleID}, false, 0, "", time.Since(t0), http.StatusBadRequest, "schedule_id required")
		writeError(w, http.StatusBadRequest, "schedule_id required and must be > 0", "bad_request")
		return
	}
	args := map[string]any{"schedule_id": body.ScheduleID}

	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/book", args, false, body.ScheduleID, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/book", args)
		return
	}

	mid, err := ensureMembershipUserID(r.Context(), s.client)
	if err != nil {
		s.auditMutation(r, "/api/v1/book", args, true, body.ScheduleID, "FAILED", time.Since(t0), http.StatusBadGateway, err.Error())
		writeError(w, http.StatusBadGateway, err.Error(), "membership")
		return
	}

	bookerMu.Lock()
	res, berr := s.client.BookClass(r.Context(), mid, body.ScheduleID, false)
	bookerMu.Unlock()
	out := mutationResultJSON(res)
	if berr != nil {
		s.auditMutation(r, "/api/v1/book", args, true, body.ScheduleID, "FAILED", time.Since(t0), out.StatusCode, berr.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  berr.Error(),
			"code":   "upstream",
			"result": out,
		})
		return
	}
	s.auditMutation(r, "/api/v1/book", args, true, body.ScheduleID, "BOOKED", time.Since(t0), out.StatusCode, "")
	writeJSON(w, http.StatusOK, map[string]any{"result": "BOOKED", "request": args, "upstream": out})
}

// --- /cancel ---

type cancelReq struct {
	ScheduleUserID int `json:"schedule_user_id"`
}

func (s *apiServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	var body cancelReq
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if body.ScheduleUserID <= 0 {
		writeError(w, http.StatusBadRequest, "schedule_user_id required and must be > 0", "bad_request")
		return
	}
	args := map[string]any{"schedule_user_id": body.ScheduleUserID}
	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/cancel", args, false, 0, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/cancel", args)
		return
	}
	bookerMu.Lock()
	res, err := s.client.CancelBooking(r.Context(), body.ScheduleUserID, false)
	bookerMu.Unlock()
	out := mutationResultJSON(res)
	if err != nil {
		s.auditMutation(r, "/api/v1/cancel", args, true, 0, "FAILED", time.Since(t0), out.StatusCode, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "code": "upstream", "result": out})
		return
	}
	s.auditMutation(r, "/api/v1/cancel", args, true, 0, "CANCELLED", time.Since(t0), out.StatusCode, "")
	writeJSON(w, http.StatusOK, map[string]any{"result": "CANCELLED", "request": args, "upstream": out})
}

// --- /waitlist ---

type waitlistReq struct {
	ScheduleID int `json:"schedule_id"`
}

func (s *apiServer) handleWaitlist(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	var body waitlistReq
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if body.ScheduleID <= 0 {
		writeError(w, http.StatusBadRequest, "schedule_id required and must be > 0", "bad_request")
		return
	}
	args := map[string]any{"schedule_id": body.ScheduleID}
	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/waitlist", args, false, body.ScheduleID, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/waitlist", args)
		return
	}
	mid, err := ensureMembershipUserID(r.Context(), s.client)
	if err != nil {
		s.auditMutation(r, "/api/v1/waitlist", args, true, body.ScheduleID, "FAILED", time.Since(t0), http.StatusBadGateway, err.Error())
		writeError(w, http.StatusBadGateway, err.Error(), "membership")
		return
	}
	bookerMu.Lock()
	res, werr := s.client.JoinWaitlist(r.Context(), mid, body.ScheduleID, false)
	bookerMu.Unlock()
	out := mutationResultJSON(res)
	if werr != nil {
		s.auditMutation(r, "/api/v1/waitlist", args, true, body.ScheduleID, "FAILED", time.Since(t0), out.StatusCode, werr.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": werr.Error(), "code": "upstream", "result": out})
		return
	}
	s.auditMutation(r, "/api/v1/waitlist", args, true, body.ScheduleID, "WAITLIST", time.Since(t0), out.StatusCode, "")
	writeJSON(w, http.StatusOK, map[string]any{"result": "WAITLIST", "request": args, "upstream": out})
}

// --- /waitlist/leave ---

type waitlistLeaveReq struct {
	StandbyID int `json:"standby_id"`
}

func (s *apiServer) handleWaitlistLeave(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	var body waitlistLeaveReq
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if body.StandbyID <= 0 {
		writeError(w, http.StatusBadRequest, "standby_id required and must be > 0", "bad_request")
		return
	}
	args := map[string]any{"standby_id": body.StandbyID}
	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/waitlist/leave", args, false, 0, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/waitlist/leave", args)
		return
	}
	bookerMu.Lock()
	res, err := s.client.LeaveWaitlist(r.Context(), body.StandbyID, false)
	bookerMu.Unlock()
	out := mutationResultJSON(res)
	if err != nil {
		s.auditMutation(r, "/api/v1/waitlist/leave", args, true, 0, "FAILED", time.Since(t0), out.StatusCode, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "code": "upstream", "result": out})
		return
	}
	s.auditMutation(r, "/api/v1/waitlist/leave", args, true, 0, "WAITLIST_LEFT", time.Since(t0), out.StatusCode, "")
	writeJSON(w, http.StatusOK, map[string]any{"result": "WAITLIST_LEFT", "request": args, "upstream": out})
}

// --- /pause ---

type pauseReq struct {
	Duration string `json:"duration,omitempty"`
	Until    string `json:"until,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func (s *apiServer) handlePause(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	var body pauseReq
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	args := map[string]any{
		"duration": body.Duration, "until": body.Until, "reason": body.Reason,
	}
	cfg, err := s.cfgReload()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "config")
		return
	}
	loc := cfg.Location()
	now := time.Now().In(loc)

	until, perr := resolvePauseUntil(body, now, loc)
	if perr != nil {
		s.auditMutation(r, "/api/v1/pause", args, hasConfirm(r), 0, "FAILED", time.Since(t0), http.StatusBadRequest, perr.Error())
		writeError(w, http.StatusBadRequest, perr.Error(), "bad_request")
		return
	}

	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/pause", args, false, 0, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/pause", map[string]any{
			"would_pause_until": until,
			"reason":            strings.TrimSpace(body.Reason),
		})
		return
	}
	st := pauseState{PausedUntil: until, Reason: strings.TrimSpace(body.Reason), UpdatedAt: now}
	if err := writePauseState(st); err != nil {
		s.auditMutation(r, "/api/v1/pause", args, true, 0, "FAILED", time.Since(t0), http.StatusInternalServerError, err.Error())
		writeError(w, http.StatusInternalServerError, err.Error(), "fs_error")
		return
	}
	s.auditMutation(r, "/api/v1/pause", args, true, 0, "PAUSED", time.Since(t0), http.StatusOK, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"result":  "PAUSED",
		"until":   until,
		"summary": st.Summary(now, loc),
	})
}

// resolvePauseUntil consolidates the two body shapes:
//   {duration: "3d"} | {until: RFC3339}
//
// Empty body -> 24h from now (matches /pause CLI behavior).
func resolvePauseUntil(body pauseReq, now time.Time, loc *time.Location) (time.Time, error) {
	dur := strings.TrimSpace(body.Duration)
	until := strings.TrimSpace(body.Until)
	if dur == "" && until == "" {
		return now.Add(24 * time.Hour).In(loc), nil
	}
	if until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return time.Time{}, fmt.Errorf("bad until: %w", err)
		}
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("until is in the past")
		}
		return t.In(loc), nil
	}
	d, err := parseShortDuration(dur)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad duration: %w", err)
	}
	if d <= 0 || d > 90*24*time.Hour {
		return time.Time{}, fmt.Errorf("duration out of range (1m..90d)")
	}
	return now.Add(d).In(loc), nil
}

// --- /resume ---

func (s *apiServer) handleResume(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/resume", nil, false, 0, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/resume", map[string]any{})
		return
	}
	if err := clearPauseState(); err != nil {
		s.auditMutation(r, "/api/v1/resume", nil, true, 0, "FAILED", time.Since(t0), http.StatusInternalServerError, err.Error())
		writeError(w, http.StatusInternalServerError, err.Error(), "fs_error")
		return
	}
	s.auditMutation(r, "/api/v1/resume", nil, true, 0, "RESUMED", time.Since(t0), http.StatusOK, "")
	writeJSON(w, http.StatusOK, map[string]any{"result": "RESUMED"})
}

// --- /plan/day ---

type planDayReq struct {
	Weekday string    `json:"weekday"`
	Options []planOpt `json:"options"`
}

func (s *apiServer) handlePlanDay(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	var body planDayReq
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	wd := strings.ToLower(strings.TrimSpace(body.Weekday))
	if _, ok := dayKeyToWeekday[wd]; !ok {
		writeError(w, http.StatusBadRequest, "weekday must be one of sunday..saturday", "bad_request")
		return
	}
	if len(body.Options) == 0 {
		writeError(w, http.StatusBadRequest, "options must be a non-empty array", "bad_request")
		return
	}
	args := map[string]any{"weekday": wd, "options": body.Options}
	dayCfg := config.DayConfig{Enabled: true}
	for _, o := range body.Options {
		dayCfg.Options = append(dayCfg.Options, config.ClassOption{
			Time: o.Time, Category: o.Category,
		})
	}
	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/plan/day", args, false, 0, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/plan/day", args)
		return
	}
	if err := mergeUserPlanDay(wd, dayCfg); err != nil {
		s.auditMutation(r, "/api/v1/plan/day", args, true, 0, "FAILED", time.Since(t0), http.StatusInternalServerError, err.Error())
		writeError(w, http.StatusInternalServerError, err.Error(), "fs_error")
		return
	}
	s.auditMutation(r, "/api/v1/plan/day", args, true, 0, "PLAN_UPDATED", time.Since(t0), http.StatusOK, "")
	writeJSON(w, http.StatusOK, map[string]any{"result": "PLAN_UPDATED", "weekday": wd, "options": body.Options})
}

// --- /plan/clear ---

type planClearReq struct {
	Weekday string `json:"weekday"`
}

func (s *apiServer) handlePlanClear(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	var body planClearReq
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	wd := strings.ToLower(strings.TrimSpace(body.Weekday))
	if _, ok := dayKeyToWeekday[wd]; !ok {
		writeError(w, http.StatusBadRequest, "weekday must be one of sunday..saturday", "bad_request")
		return
	}
	args := map[string]any{"weekday": wd}
	if !hasConfirm(r) {
		s.auditMutation(r, "/api/v1/plan/clear", args, false, 0, "DRY_RUN", time.Since(t0), http.StatusOK, "")
		writeDryRun(w, "/api/v1/plan/clear", args)
		return
	}
	if err := mergeUserPlanDay(wd, config.DayConfig{Enabled: false}); err != nil {
		s.auditMutation(r, "/api/v1/plan/clear", args, true, 0, "FAILED", time.Since(t0), http.StatusInternalServerError, err.Error())
		writeError(w, http.StatusInternalServerError, err.Error(), "fs_error")
		return
	}
	s.auditMutation(r, "/api/v1/plan/clear", args, true, 0, "PLAN_CLEARED", time.Since(t0), http.StatusOK, "")
	writeJSON(w, http.StatusOK, map[string]any{"result": "PLAN_CLEARED", "weekday": wd})
}

// mergeUserPlanDay reads user_plan.yaml, replaces the given weekday, and
// writes it back. Validates the merged config (config.yaml + user_plan)
// before persisting so we never write a plan that the daemon rejects.
func mergeUserPlanDay(weekday string, day config.DayConfig) error {
	wrap := struct {
		Days map[string]config.DayConfig `yaml:"days"`
	}{Days: map[string]config.DayConfig{}}

	path := userPlanOverlayPath()
	if existing, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(existing, &wrap)
		if wrap.Days == nil {
			wrap.Days = map[string]config.DayConfig{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	wrap.Days[weekday] = day

	c2, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	for k, v := range wrap.Days {
		c2.Days[k] = v
	}
	if err := c2.Validate(); err != nil {
		return fmt.Errorf("merged config invalid: %w", err)
	}

	raw, err := yaml.Marshal(&wrap)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ----- helpers -----------------------------------------------------------

func queryIntDefault(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// mutationResultJSON projects an arboxapi.MutationResult into a small
// API-friendly shape (no raw bytes, no internal fields).
type mutationJSON struct {
	URL         string `json:"url"`
	Method      string `json:"method"`
	StatusCode  int    `json:"status_code"`
	Sent        bool   `json:"sent"`
	RequestJSON string `json:"request_json,omitempty"`
	Message     string `json:"message,omitempty"`
}

func mutationResultJSON(res *arboxapi.MutationResult) mutationJSON {
	if res == nil {
		return mutationJSON{}
	}
	return mutationJSON{
		URL: res.URL, Method: res.Method,
		StatusCode: res.StatusCode, Sent: res.Sent,
		RequestJSON: res.RequestJSON, Message: res.Message,
	}
}
