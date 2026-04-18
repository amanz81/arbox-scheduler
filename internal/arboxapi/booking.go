package arboxapi

// This file wraps the member-API endpoints that MUTATE state: book a class,
// cancel a booking, join/leave a waitlist (Arbox calls it "stand by"), and
// membership/feed lookups needed to construct those requests.
//
// Endpoint provenance:
//
//   - POST /api/v2/scheduleUser/insert            BOOK          confirmed [1]
//   - GET  /api/v2/boxes/<box_id>/memberships/1   MEMBERSHIP    confirmed [1]
//   - GET  /api/v2/user/feed                      USER FEED     confirmed [1]
//   - POST /api/v2/scheduleUser/cancel            CANCEL        BEST GUESS — not yet
//                                                                verified against live API
//   - POST /api/v2/standBy/insert                 WAITLIST+     BEST GUESS
//   - POST /api/v2/standBy/cancel                 WAITLIST-     BEST GUESS
//
// [1] https://github.com/oribenez/auto-enroll-arbox/blob/master/lib/arbox.js
//
// CancelBooking / JoinWaitlist / LeaveWaitlist have a `DryRun` flag baked in
// so callers can render exactly what would be sent before we confirm the real
// endpoint. The network code still runs when DryRun=false; it just means the
// error message you'll probably see first is "404 Not Found", which is how we
// find the real path.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// strconvAtoi is a local alias so the helpers above don't force a user-facing
// import of "strconv" from every call site.
func strconvAtoi(s string) (int, error) { return strconv.Atoi(s) }

// ----- MEMBERSHIP --------------------------------------------------------

// Membership is a single membership entry for the authenticated user at a
// given box. Arbox usually returns one per user per gym.
//
// Field shape verified empirically against /api/v2/boxes/<box_id>/memberships/1.
type Membership struct {
	ID     int `json:"id"`       // membership_user_id — needed for booking
	UserFK int `json:"user_fk"`  // internal user id
	BoxFK  int `json:"box_fk"`
	Active int `json:"active"`   // 1 = active

	Start        string `json:"start"`         // YYYY-MM-DD
	End          *string `json:"end"`          // null for recurring plans
	SessionsLeft *int   `json:"sessions_left"` // null for unlimited
	MtType       string `json:"mt_type"`       // "plan" / "sessions" etc.

	MembershipTypes struct {
		ID            int    `json:"id"`
		Name          string `json:"name"` // e.g. "עד חמישה אימונים בשבוע"
		Type          string `json:"type"`
		LocationBoxFK int    `json:"location_box_fk"`
		Price         int    `json:"price"`
		Sessions      *int   `json:"sessions"`
	} `json:"membership_types"`
}

// PlanName returns the human-readable plan name (from membership_types.name).
func (m Membership) PlanName() string { return m.MembershipTypes.Name }

// StatusLabel returns a compact active/inactive label.
func (m Membership) StatusLabel() string {
	if m.Active == 1 {
		return "active"
	}
	return "inactive"
}

// GetMembership fetches the user's memberships at the given box and returns
// the first one. That membership's ID is the `membership_user_id` required by
// the booking endpoint.
func (c *Client) GetMembership(ctx context.Context, boxID int) (*Membership, error) {
	path := fmt.Sprintf("/api/v2/boxes/%d/memberships/1", boxID)
	var env struct {
		Data []Membership `json:"data"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, fmt.Errorf("get membership: %w", err)
	}
	if len(env.Data) == 0 {
		return nil, fmt.Errorf("no memberships returned for box %d", boxID)
	}
	return &env.Data[0], nil
}

// ----- FEED (quota) ------------------------------------------------------

// UserFeed is a small slice of /api/v2/user/feed — enough to show "X classes
// left this month". The real response is much larger.
//
// Past/Future are strings in the API payload (e.g. "3"), not ints, so we
// store them as strings and expose helpers.
type UserFeed struct {
	ScheduleUserStatus struct {
		Results struct {
			Past   string `json:"past"`
			Future string `json:"future"`
		} `json:"results"`
	} `json:"scheduleUserStatus"`
	Raw map[string]any `json:"-"`
}

// PastBookings / FutureBookings parse the string counters to ints.
// Unparseable values (missing field, null) return 0.
func (f UserFeed) PastBookings() int   { n, _ := strconvAtoi(f.ScheduleUserStatus.Results.Past); return n }
func (f UserFeed) FutureBookings() int { n, _ := strconvAtoi(f.ScheduleUserStatus.Results.Future); return n }

// GetFeed fetches the authenticated user's feed (includes quota counters).
func (c *Client) GetFeed(ctx context.Context) (*UserFeed, error) {
	var raw json.RawMessage
	if _, err := c.doJSON(ctx, http.MethodGet, "/api/v2/user/feed", nil, &raw); err != nil {
		return nil, fmt.Errorf("get feed: %w", err)
	}
	var f UserFeed
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("decode feed: %w", err)
	}
	var rawMap map[string]any
	_ = json.Unmarshal(raw, &rawMap)
	f.Raw = rawMap
	return &f, nil
}

// ----- BOOKING -----------------------------------------------------------

// BookRequest is the JSON body sent to /api/v2/scheduleUser/insert.
type BookRequest struct {
	Extras           any `json:"extras"` // always nil in reference impl
	MembershipUserID int `json:"membership_user_id"`
	ScheduleID       int `json:"schedule_id"`
}

// MutationResult captures what happened on a write call: the URL, the
// serialized request body (pretty-printed), whether it was actually sent,
// the HTTP status code, and the raw response body.
type MutationResult struct {
	Method     string
	URL        string
	RequestJSON string
	Sent        bool
	StatusCode  int
	ResponseRaw json.RawMessage
	// Message is a human-readable Arbox error when the call failed.
	Message string
}

// BookClass books the given class for the authenticated user. If dryRun is
// true the request is built and returned in the MutationResult but never sent.
//
// On a real (non-dry) success, StatusCode == 200 and ResponseRaw contains the
// server's payload (usually `{data: {...booking object...}}`).
func (c *Client) BookClass(ctx context.Context, membershipUserID, scheduleID int, dryRun bool) (*MutationResult, error) {
	req := BookRequest{
		Extras:           nil,
		MembershipUserID: membershipUserID,
		ScheduleID:       scheduleID,
	}
	body, _ := json.MarshalIndent(req, "", "  ")
	res := &MutationResult{
		Method:      http.MethodPost,
		URL:         c.BaseURL + "/api/v2/scheduleUser/insert",
		RequestJSON: string(body),
	}
	if dryRun {
		return res, nil
	}
	return c.sendMutation(ctx, res, http.MethodPost, "/api/v2/scheduleUser/insert", req)
}

// CancelBooking cancels an existing booking by its schedule_user id.
//
// Endpoint is a best guess (`POST /api/v2/scheduleUser/cancel` with
// {schedule_user_id}). If that turns out to be wrong the real request will
// 404; the returned MutationResult shows exactly what was attempted so we
// can tweak the path/body once we observe the right one.
func (c *Client) CancelBooking(ctx context.Context, scheduleUserID int, dryRun bool) (*MutationResult, error) {
	body := map[string]int{"schedule_user_id": scheduleUserID}
	reqJSON, _ := json.MarshalIndent(body, "", "  ")
	res := &MutationResult{
		Method:      http.MethodPost,
		URL:         c.BaseURL + "/api/v2/scheduleUser/cancel",
		RequestJSON: string(reqJSON),
	}
	if dryRun {
		return res, nil
	}
	return c.sendMutation(ctx, res, http.MethodPost, "/api/v2/scheduleUser/cancel", body)
}

// JoinWaitlist puts the user on the standby list for a class that's full.
// Endpoint is a best guess until we see the real one.
func (c *Client) JoinWaitlist(ctx context.Context, membershipUserID, scheduleID int, dryRun bool) (*MutationResult, error) {
	body := map[string]int{
		"membership_user_id": membershipUserID,
		"schedule_id":        scheduleID,
	}
	reqJSON, _ := json.MarshalIndent(body, "", "  ")
	res := &MutationResult{
		Method:      http.MethodPost,
		URL:         c.BaseURL + "/api/v2/standBy/insert",
		RequestJSON: string(reqJSON),
	}
	if dryRun {
		return res, nil
	}
	return c.sendMutation(ctx, res, http.MethodPost, "/api/v2/standBy/insert", body)
}

// LeaveWaitlist removes the user from a standby list. `standbyID` is what
// the schedule payload returns as `user_in_standby`.
// Endpoint is a best guess until we see the real one.
func (c *Client) LeaveWaitlist(ctx context.Context, standbyID int, dryRun bool) (*MutationResult, error) {
	body := map[string]int{"stand_by_id": standbyID}
	reqJSON, _ := json.MarshalIndent(body, "", "  ")
	res := &MutationResult{
		Method:      http.MethodPost,
		URL:         c.BaseURL + "/api/v2/standBy/cancel",
		RequestJSON: string(reqJSON),
	}
	if dryRun {
		return res, nil
	}
	return c.sendMutation(ctx, res, http.MethodPost, "/api/v2/standBy/cancel", body)
}

// sendMutation is shared by all the write helpers. It uses doJSON's retry-on-
// 401 logic and decodes the response into MutationResult.
func (c *Client) sendMutation(ctx context.Context, res *MutationResult, method, path string, body any) (*MutationResult, error) {
	var raw json.RawMessage
	resp, err := c.doJSON(ctx, method, path, body, &raw)
	if resp != nil {
		res.StatusCode = resp.StatusCode
	}
	res.Sent = true
	res.ResponseRaw = raw
	if err != nil {
		// Try to pluck a human-friendly error message out of the body.
		res.Message = extractErrorMessage(raw)
		return res, err
	}
	return res, nil
}

// extractErrorMessage looks for Arbox's `error.messageToUser` or
// `message` fields in a response body, to put in our MutationResult.
func extractErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if e, ok := m["error"].(map[string]any); ok {
		if s, ok := e["messageToUser"].(string); ok && s != "" {
			return s
		}
		if s, ok := e["message"].(string); ok && s != "" {
			return s
		}
	}
	if s, ok := m["message"].(string); ok && s != "" {
		return s
	}
	return ""
}
