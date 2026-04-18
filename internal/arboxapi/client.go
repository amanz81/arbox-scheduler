// Package arboxapi is a thin HTTP client for the (unofficial) Arbox MEMBER API.
//
// The member app at https://site.arboxapp.com (and the mobile apps) talk to a
// different backend than the gym-owner panel:
//
//   - Member backend:  https://apiappv2.arboxapp.com/api/v2/...
//   - Panel backend:   https://api.arboxapp.com/index.php/api/v1/...  (NOT used)
//
// Reference: https://github.com/oribenez/auto-enroll-arbox (member-side
// auto-enroll tool). Endpoints here were copied/adapted from that project and
// are unofficial; Arbox can change them at any time.
//
// Auth model:
//   - POST /api/v2/user/login with {email, password} returns
//     {data: {token, refreshToken, ...}}.
//   - Every subsequent call sends BOTH headers:
//         accesstoken: <token>
//         refreshtoken: <refreshToken>
//   - When the access token expires, the server returns 401; we simply call
//     Login again with the stored email+password. The refresh token is also
//     stored but we don't (yet) know the dedicated refresh endpoint, so the
//     simplest recovery is re-login.
package arboxapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the Arbox member backend. Override via env ARBOX_BASE_URL
// only if the API moves.
const DefaultBaseURL = "https://apiappv2.arboxapp.com"

// Client is a low-level HTTP client for the member API. Safe for concurrent
// use as long as Token/RefreshToken aren't mutated from multiple goroutines.
type Client struct {
	BaseURL      string
	HTTP         *http.Client
	Token        string // JWT access token (accesstoken header)
	RefreshToken string // refresh token (refreshtoken header)
	UserAgent    string

	// creds is set by SetCredentials; used by doJSON to auto re-login on 401.
	creds *Credentials
}

// New returns a Client with sensible defaults.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		UserAgent: "arbox-scheduler/0.1 (+https://github.com/amanz81/arbox-scheduler)",
	}
}

// LoginResponse is the subset of the session response we care about. The full
// payload includes user profile, locale, etc. — accessible via Raw.
type LoginResponse struct {
	Token        string
	RefreshToken string
	UserID       int
	Email        string
	FirstName    string
	LastName     string
	Raw          map[string]any // full decoded JSON for debugging / discovery
}

// ErrUnauthorized indicates the server rejected credentials or token.
var ErrUnauthorized = errors.New("arbox: unauthorized")

// ErrLoginFailed indicates the login call did not return a token (bad creds,
// changed API shape, etc.).
var ErrLoginFailed = errors.New("arbox: login did not return a token")

// loginEnvelope matches the Arbox member login response shape:
//
//	{ "data": { "token": "...", "refreshToken": "...", "id": 123, ... } }
type loginEnvelope struct {
	Data json.RawMessage `json:"data"`
}

// Login POSTs credentials to the member session endpoint and returns the token
// pair plus basic profile fields. On success, c.Token and c.RefreshToken are
// set so subsequent calls on this client are authenticated.
func (c *Client) Login(ctx context.Context, email, password string) (*LoginResponse, error) {
	if email == "" || password == "" {
		return nil, fmt.Errorf("arbox: email and password are required")
	}

	body, err := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return nil, err
	}

	endpoint := c.BaseURL + "/api/v2/user/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.applyCommonHeaders(req, "" /* token not sent on login */, "")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arbox login: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (status %d): %s", ErrUnauthorized, resp.StatusCode, snippet(respBody))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("arbox login: http %d: %s", resp.StatusCode, snippet(respBody))
	}

	return parseLoginResponse(respBody)
}

// parseLoginResponse extracts fields from the Arbox member login payload.
// Split out for easier testing with recorded responses.
func parseLoginResponse(respBody []byte) (*LoginResponse, error) {
	var env loginEnvelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("arbox login: decode envelope: %w (body=%s)", err, snippet(respBody))
	}

	// Older / alternative shapes may omit the wrapping "data" and put fields
	// at the top level. Try both.
	inner := env.Data
	if len(inner) == 0 {
		inner = respBody
	}

	var raw map[string]any
	if err := json.Unmarshal(inner, &raw); err != nil {
		return nil, fmt.Errorf("arbox login: decode data: %w (body=%s)", err, snippet(respBody))
	}

	lr := &LoginResponse{Raw: raw}
	lr.Token, _ = raw["token"].(string)
	lr.RefreshToken, _ = raw["refreshToken"].(string)
	lr.Email, _ = raw["email"].(string)
	lr.FirstName, _ = raw["first_name"].(string)
	lr.LastName, _ = raw["last_name"].(string)
	if n, ok := raw["id"].(float64); ok {
		lr.UserID = int(n)
	}

	if lr.Token == "" {
		return nil, fmt.Errorf("%w: body=%s", ErrLoginFailed, snippet(respBody))
	}
	return lr, nil
}

// setTokensFrom populates Client auth fields from a successful login.
func (c *Client) setTokensFrom(r *LoginResponse) {
	c.Token = r.Token
	c.RefreshToken = r.RefreshToken
}

// LoginAndStore is a convenience: Login + store tokens on the client.
func (c *Client) LoginAndStore(ctx context.Context, email, password string) (*LoginResponse, error) {
	r, err := c.Login(ctx, email, password)
	if err != nil {
		return nil, err
	}
	c.setTokensFrom(r)
	return r, nil
}

// applyCommonHeaders sets the Arbox member-app headers on a request. Empty
// token/refresh values are skipped (used during login).
func (c *Client) applyCommonHeaders(req *http.Request, token, refresh string) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if token != "" {
		req.Header.Set("accesstoken", token)
	}
	if refresh != "" {
		req.Header.Set("refreshtoken", refresh)
	}
}

// snippet trims response bodies for error messages so we never dump secrets /
// giant payloads to logs.
func snippet(b []byte) string {
	const max = 300
	s := string(b)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
