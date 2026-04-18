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

// YouStatus returns a short label for the user's relationship to this class.
func (c Class) YouStatus() string {
	switch {
	case c.UserBookedID != nil:
		return "BOOKED"
	case c.UserStandByID != nil:
		return "WAITLIST"
	default:
		return ""
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

// GetScheduleDay fetches a single day's classes. Use this when the API turns
// out to only return one day at a time regardless of `to`.
func (c *Client) GetScheduleDay(ctx context.Context, day time.Time, locationsBoxID int) ([]Class, error) {
	// Use midnight in the *same location* as `day` so the calendar date (y,m,d)
	// matches the member's box timezone. Converting y,m,d to UTC midnight was
	// wrong (off-by-one vs local days) and led to empty or mismatched classes.
	y, m, d := day.Date()
	loc := day.Location()
	if loc == nil {
		loc = time.UTC
	}
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	return c.GetSchedule(ctx, midnight, midnight, locationsBoxID)
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
