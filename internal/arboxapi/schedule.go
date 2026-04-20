package arboxapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Class is a single class (lesson) as returned by Arbox's betweenDates
// endpoint. Arbox returns many more fields than these; we decode only what we
// care about. Keep the Raw field for debugging / future use.
//
// Field names below are what the API actually returns (verified empirically
// against apiappv2.arboxapp.com). A sibling "has_spots" field exists but
// appears unreliable — use Free instead.
type Class struct {
	ID       int    `json:"id"`        // schedule_id (used when booking)
	Date     string `json:"date"`      // "YYYY-MM-DD"
	Time     string `json:"time"`      // "HH:MM"
	EndTime  string `json:"end_time"`  // "HH:MM"
	MaxUsers int    `json:"max_users"` // capacity

	Registered int `json:"registered"` // current booked count
	Free       int `json:"free"`       // spots remaining = MaxUsers - Registered
	StandBy    int `json:"stand_by"`   // current waitlist count

	// UserBookedID is non-nil when the authenticated user is confirmed on
	// this class. It holds the schedule_user id (needed for cancel).
	UserBookedID *int `json:"user_booked"`
	// UserStandByID is non-nil when the user is on this class's waitlist.
	UserStandByID *int `json:"user_in_standby"`
	// StandByPosition is the user's 1-based ordinal on the waitlist (1 = next
	// in line). Arbox populates this only when `user_in_standby` is set. Zero
	// means "not on waitlist" OR "API didn't return this field for this
	// account" — treat 0 as "unknown".
	StandByPosition int `json:"stand_by_position"`

	// DisableCancellationMinutes is how many minutes before class start you
	// can no longer cancel (0 if always cancellable).
	DisableCancellationMinutes int `json:"disable_cancellation_time"`
	// EnableRegistrationHours is how many hours before class start the
	// booking window opens (e.g. 24 for most days, 48 for Sunday).
	EnableRegistrationHours int `json:"enable_registration_time"`

	BoxCategories struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"box_categories"`

	Coach *struct {
		FullName  string `json:"full_name"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	} `json:"coach"`

	Raw map[string]any `json:"-"`
}

// ResolvedCategoryName returns the best-effort class title for filtering and
// display. Some Arbox payloads leave box_categories.name empty but keep an
// alternate shape in Raw.
func (c Class) ResolvedCategoryName() string {
	s := strings.TrimSpace(c.BoxCategories.Name)
	if s != "" {
		return s
	}
	if c.Raw == nil {
		return ""
	}
	for _, k := range []string{"class_name", "lesson_name", "name", "title"} {
		if v, ok := c.Raw[k]; ok {
			if str, ok := v.(string); ok {
				if t := strings.TrimSpace(str); t != "" {
					return t
				}
			}
		}
	}
	if v, ok := c.Raw["box_categories"]; ok {
		switch m := v.(type) {
		case map[string]any:
			if n, ok := m["name"].(string); ok {
				return strings.TrimSpace(n)
			}
		}
	}
	return ""
}

// YouStatus returns a short label for the user's relationship to this class.
func (c Class) YouStatus() string {
	if c.UserBookedID != nil {
		return "BOOKED"
	}
	if c.UserStandByID != nil {
		return "WAITLIST"
	}
	if c.Raw != nil {
		if bookingFromRaw(c.Raw) {
			return "BOOKED"
		}
		if standbyFromRaw(c.Raw) {
			return "WAITLIST"
		}
	}
	return ""
}

// YouStatusDetail returns YouStatus plus a human-readable position suffix
// like "WAITLIST 3/7" when the user is on the waitlist AND we know the
// ordinal + total. Falls back to plain "WAITLIST" when the position is
// unknown, or "BOOKED" / "" as YouStatus would return.
func (c Class) YouStatusDetail() string {
	base := c.YouStatus()
	if base != "WAITLIST" {
		return base
	}
	pos := c.StandByPosition
	if pos == 0 && c.Raw != nil {
		// Some Arbox responses keep it only in Raw.
		if v, ok := c.Raw["stand_by_position"]; ok {
			switch x := v.(type) {
			case float64:
				pos = int(x)
			case int:
				pos = x
			}
		}
	}
	if pos > 0 && c.StandBy > 0 {
		return fmt.Sprintf("%s %d/%d", base, pos, c.StandBy)
	}
	if pos > 0 {
		return fmt.Sprintf("%s #%d", base, pos)
	}
	return base
}

func bookingFromRaw(raw map[string]any) bool {
	for _, k := range []string{
		"user_booked", "userBooked", "schedule_user_id", "scheduleUserId",
		"member_schedule_user_id",
	} {
		if v, ok := raw[k]; ok && isPositiveBookingRef(v) {
			return true
		}
	}
	if v, ok := raw["is_booked"]; ok && truthyScalar(v) {
		return true
	}
	if v, ok := raw["booked"]; ok && truthyScalar(v) {
		return true
	}
	return false
}

func standbyFromRaw(raw map[string]any) bool {
	for _, k := range []string{"user_in_standby", "userInStandby", "stand_by_user", "standByUser"} {
		if v, ok := raw[k]; ok && isPositiveBookingRef(v) {
			return true
		}
	}
	if v, ok := raw["in_standby"]; ok && truthyScalar(v) {
		return true
	}
	return false
}

func isPositiveBookingRef(v any) bool {
	switch x := v.(type) {
	case float64:
		return x > 0
	case int:
		return x > 0
	case int64:
		return x > 0
	case string:
		s := strings.TrimSpace(x)
		if s == "" || s == "0" {
			return false
		}
		// non-empty id-like string from API
		return true
	case map[string]any:
		if id, ok := x["id"]; ok {
			return isPositiveBookingRef(id)
		}
		if id, ok := x["schedule_user_id"]; ok {
			return isPositiveBookingRef(id)
		}
		return false
	default:
		return false
	}
}

func truthyScalar(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "1" || s == "true" || s == "yes"
	default:
		return false
	}
}

// Location describes one gym / one location of a gym as returned by
// /api/v2/boxes/locations. The shape is { data: [ { id, name, locations_box: [ {id, name, ...} ] } ] }.
type Location struct {
	BoxID           int    `json:"id"`
	BoxName         string `json:"name"`
	LocationsBox    []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"locations_box"`
}

// GetLocations returns the list of boxes (gyms) the authenticated member
// belongs to, each with its locations. For a typical member this is one entry.
func (c *Client) GetLocations(ctx context.Context) ([]Location, error) {
	var env struct {
		Data []Location `json:"data"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/api/v2/boxes/locations", nil, &env); err != nil {
		return nil, fmt.Errorf("get locations: %w", err)
	}
	return env.Data, nil
}

// ScheduleParams is the body of the /api/v2/schedule/betweenDates call.
// `from` and `to` are ISO-8601 timestamps like "2026-04-20T00:00:00.000Z".
type ScheduleParams struct {
	From           string `json:"from"`
	To             string `json:"to"`
	LocationsBoxID int    `json:"locations_box_id"`
}

// GetSchedule fetches classes between two dates (inclusive, UTC midnight) for
// the given locations_box_id. `from` and `to` should be time.Time at local
// midnight; this helper serializes them as UTC ISO-8601.
//
// Note: the reference implementation (auto-enroll-arbox) calls this endpoint
// with the same date for `from` and `to` to get a single day. We try with a
// real range first; if Arbox ignores `to` and only returns one day, callers
// can loop day-by-day.
func (c *Client) GetSchedule(ctx context.Context, from, to time.Time, locationsBoxID int) ([]Class, error) {
	params := ScheduleParams{
		From:           from.UTC().Format("2006-01-02T15:04:05.000Z"),
		To:             to.UTC().Format("2006-01-02T15:04:05.000Z"),
		LocationsBoxID: locationsBoxID,
	}

	// Decode into an envelope of raw messages so we can keep each class's
	// Raw map for debugging.
	var env struct {
		Data []json.RawMessage `json:"data"`
	}
	if _, err := c.doJSON(ctx, http.MethodPost, "/api/v2/schedule/betweenDates", params, &env); err != nil {
		return nil, fmt.Errorf("get schedule: %w", err)
	}
	if os.Getenv("ARBOX_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[arboxapi] betweenDates from=%s to=%s loc=%d -> %d entries\n",
			params.From, params.To, params.LocationsBoxID, len(env.Data))
	}

	out := make([]Class, 0, len(env.Data))
	for i, rm := range env.Data {
		var cls Class
		if err := json.Unmarshal(rm, &cls); err != nil {
			// Don't silently swallow — decode failures here usually mean the
			// API added a field that doesn't match our struct types.
			if os.Getenv("ARBOX_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "[arboxapi] skip class %d decode: %v (body=%s)\n",
					i, err, snippet(rm))
			}
			continue
		}
		var raw map[string]any
		_ = json.Unmarshal(rm, &raw)
		cls.Raw = raw
		out = append(out, cls)
	}
	return out, nil
}

// GetScheduleDay fetches a single day's classes by sending the calendar date
// as midnight in **UTC** (i.e. `YYYY-MM-DDT00:00:00.000Z`). That mirrors the
// reference auto-enroll-arbox implementation; sending midnight in the member's
// local timezone caused off-by-one errors (Israel midnight = previous-day
// 21:00 UTC and the API would return the wrong calendar day).
//
// `day` is read as a calendar date (year/month/day in its own Location) so
// callers can pass either UTC midnight or a local-time value with the same
// y/m/d.
func (c *Client) GetScheduleDay(ctx context.Context, day time.Time, locationsBoxID int) ([]Class, error) {
	y, m, d := day.Date()
	midnightUTC := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return c.GetSchedule(ctx, midnightUTC, midnightUTC, locationsBoxID)
}

// -----------------------------------------------------------------------------
// Low-level JSON helper with automatic re-login on 401.
// -----------------------------------------------------------------------------

// Credentials holds email+password so the client can silently re-login when
// the access token expires.
type Credentials struct {
	Email    string
	Password string
}

// SetCredentials enables auto-relogin on 401 by letting the client re-run
// /api/v2/user/login with these creds.
func (c *Client) SetCredentials(email, password string) {
	c.creds = &Credentials{Email: email, Password: password}
}

// CanAutoRelogin reports whether the client has credentials stored to retry
// login after a 401.
func (c *Client) CanAutoRelogin() bool {
	return c.creds != nil && c.creds.Email != "" && c.creds.Password != ""
}

// doJSON sends a JSON request, decodes a JSON response into `out`, and
// retries once on 401 using SetCredentials (if any).
//
// It returns the final *http.Response (body already drained) so callers can
// inspect status codes if they want. `body` may be nil.
func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		resp, respBody, err := c.do(ctx, method, path, body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && c.CanAutoRelogin() {
			if _, err := c.LoginAndStore(ctx, c.creds.Email, c.creds.Password); err != nil {
				return resp, fmt.Errorf("auto re-login failed: %w", err)
			}
			continue
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return resp, fmt.Errorf("%w (status %d): %s", ErrUnauthorized, resp.StatusCode, snippet(respBody))
		case resp.StatusCode < 200 || resp.StatusCode >= 300:
			return resp, fmt.Errorf("arbox %s %s: http %d: %s", method, path, resp.StatusCode, snippet(respBody))
		}
		if out != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return resp, fmt.Errorf("decode response: %w (body=%s)", err, snippet(respBody))
			}
		}
		return resp, nil
	}
	return nil, errors.New("arbox: exhausted retries")
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(b)
	}
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, nil, err
	}
	c.applyCommonHeaders(req, c.Token, c.RefreshToken)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp, respBody, nil
}

// Raw is the escape-hatch request primitive for the LLM passthrough
// (GET/POST /api/v1/arbox/query). It runs the same auto-relogin-on-401
// and Cloudflare-passing-headers plumbing doJSON has, but returns the
// raw upstream status + body without any JSON decoding — so an LLM can
// hit Arbox routes we don't yet wrap in typed helpers (e.g.
// /api/v2/user/feed, /api/v2/boxes/<id>/memberships/1,
// /api/v2/notifications/...).
//
// path MUST start with "/" and is appended to BaseURL. body may be nil
// for GET or any JSON-serializable value for POST/PUT. Non-2xx responses
// are returned normally (status + body); they are not treated as errors
// — the caller (a security-conscious handler) decides what to surface.
func (c *Client) Raw(ctx context.Context, method, path string, body any) (status int, respBody []byte, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		resp, b, derr := c.do(ctx, method, path, body)
		if derr != nil {
			return 0, nil, derr
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && c.CanAutoRelogin() {
			if _, lerr := c.LoginAndStore(ctx, c.creds.Email, c.creds.Password); lerr != nil {
				return resp.StatusCode, b, nil
			}
			continue
		}
		return resp.StatusCode, b, nil
	}
	return 0, nil, errors.New("arbox: exhausted retries")
}

// int helper for JSON numbers that sometimes arrive as strings.
func atoiAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}
