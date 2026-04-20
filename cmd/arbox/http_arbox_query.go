package main

// Generic passthrough to the upstream Arbox member API.
//
// Motivation:
//   We hand-wrap the handful of endpoints that drive the daemon's own
//   booking logic (login, schedule/betweenDates, scheduleUser/insert,
//   standBy/insert, boxes/memberships). That coverage is intentionally
//   minimal. When an LLM agent using the MCP tool suite wants to answer
//   "what's in the user feed", "what memberships do I have on this box",
//   "are there notifications", etc., it needs to reach routes we haven't
//   typed yet. Shipping a typed endpoint for every Arbox route is a
//   losing race — Arbox adds + removes fields constantly.
//
//   This handler is the escape hatch: admin-token callers POST a
//   { method, path, body? } envelope and we forward it to the upstream
//   API with the daemon's existing auth + Cloudflare-passing headers.
//   The raw upstream body is returned verbatim (base64 if it isn't
//   valid JSON) so the LLM gets the same payload the Arbox web app
//   would have received.
//
// Safety posture (layered):
//
//   1. Admin-bearer only. Read-token callers hit 403 at the auth
//      middleware — same tier as /book/cancel/pause.
//   2. Path must match "^/api/v2/[a-zA-Z0-9_/\\-.?&=%]+$". We don't
//      let the LLM cross hosts (no absolute URLs accepted) or touch the
//      panel API on a different hostname.
//   3. Method must be one of GET, POST, PUT, PATCH, DELETE. HEAD /
//      OPTIONS / CONNECT are disallowed — they're of no use to the LLM
//      and widen the surface.
//   4. A small path blocklist refuses routes that would let the LLM
//      take over the account (login endpoints, token refresh, password
//      reset). The user's email+password already live in .env; we don't
//      want an agent re-logging in on a different refresh token.
//   5. Every call is written to the audit log the same way /book is.
//      client_ip + token_kind + route + method + upstream status + body
//      length + latency. Easy to grep for "what did nanobot call today".
//   6. Rate limit is the shared per-token bucket (60/min, burst 30). So
//      an LLM that gets into a polling loop can't hammer Arbox.
//
// Not covered here (intentionally): response body size cap. The
// upstream responses we care about are all < 100 KB; if Arbox ever
// returns a multi-MB payload we'll add a cap and truncation marker.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// arboxQueryReq is the JSON envelope the LLM (via arbox_api_query tool)
// or a curl caller sends. Body is passed straight through as the POST
// body when present.
type arboxQueryReq struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type arboxQueryResp struct {
	Status     int             `json:"status"`
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	JSON       json.RawMessage `json:"json,omitempty"`
	Body       string          `json:"body,omitempty"`         // base64 when !BodyIsJSON
	BodyIsJSON bool            `json:"body_is_json"`
	BodyBytes  int             `json:"body_bytes"`
}

// allowedUpstreamPath constrains passthrough targets to the member API
// namespace. Anchored, so /api/v1 on our own server or any http:// URL
// fails validation and we return 400 before reaching the upstream.
var allowedUpstreamPath = regexp.MustCompile(`^/api/v2/[A-Za-z0-9/_\-.?&=%]+$`)

// pathBlocklist — substrings that must NOT appear in an otherwise-valid
// path. Anything touching auth would let an agent effectively rotate
// the account's credentials out from under the owner.
var pathBlocklist = []string{
	"user/login",
	"user/logout",
	"user/register",
	"user/refreshToken",
	"user/recoverPassword",
	"user/changePassword",
	"user/delete",
}

// allowedUpstreamMethods keeps us to the verbs Arbox's member API uses.
// HEAD/OPTIONS/TRACE/CONNECT/PROPFIND/… add attack surface without
// value for an LLM tool caller.
var allowedUpstreamMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

func (s *apiServer) handleArboxQuery(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()

	var req arboxQueryReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "decode body: "+err.Error(), "bad_request")
		return
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if _, ok := allowedUpstreamMethods[method]; !ok {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("method %q not allowed; allowed: GET, POST, PUT, PATCH, DELETE", method),
			"bad_method")
		return
	}

	path := strings.TrimSpace(req.Path)
	if !allowedUpstreamPath.MatchString(path) {
		writeError(w, http.StatusBadRequest,
			`path must match ^/api/v2/... (member-api namespace only)`,
			"bad_path")
		return
	}
	// Defense in depth: regex allows "." (legit for e.g. file extensions)
	// but never ".." (path traversal). Keep this separate from the regex so
	// it's obvious what's guarded and why.
	if strings.Contains(path, "..") {
		writeError(w, http.StatusBadRequest,
			`path may not contain ".."`,
			"bad_path")
		return
	}
	for _, bad := range pathBlocklist {
		if strings.Contains(path, bad) {
			writeError(w, http.StatusForbidden,
				"path blocked for security: "+bad,
				"blocked_path")
			return
		}
	}

	// Forward the body as-is. nil body → GET-like call. Non-nil → JSON.
	var upstreamBody any
	if len(bytes.TrimSpace(req.Body)) > 0 && !bytes.Equal(bytes.TrimSpace(req.Body), []byte("null")) {
		// Re-decode so c.Raw (which json.Marshals body) gets the right
		// value, not a double-encoded string.
		var tmp any
		if err := json.Unmarshal(req.Body, &tmp); err != nil {
			writeError(w, http.StatusBadRequest, "body is not valid JSON: "+err.Error(), "bad_body")
			return
		}
		upstreamBody = tmp
	}

	status, raw, err := s.client.Raw(r.Context(), method, path, upstreamBody)
	latency := time.Since(t0)

	// Audit — unlike the real mutation endpoints we always audit this,
	// whether it "succeeded" or not, so there's no hole where the LLM
	// could fish without leaving a trail.
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	s.auditMutation(r, "POST /api/v1/arbox/query", map[string]any{
		"upstream_method": method,
		"upstream_path":   path,
		"body_present":    upstreamBody != nil,
	}, true, 0, "upstream", latency, status, errMsg)

	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream call: "+err.Error(), "upstream")
		return
	}

	// Try to return the upstream body as JSON when it parses. Otherwise
	// base64 so the LLM gets a faithful (if noisier) record — Arbox does
	// occasionally serve HTML challenge pages when Cloudflare acts up
	// and we want the LLM to see "this is HTML" instead of a silent
	// decode error.
	resp := arboxQueryResp{
		Status:    status,
		Method:    method,
		Path:      path,
		BodyBytes: len(raw),
	}
	if json.Valid(raw) {
		resp.JSON = raw
		resp.BodyIsJSON = true
	} else {
		resp.Body = base64.StdEncoding.EncodeToString(raw)
		resp.BodyIsJSON = false
	}
	writeJSON(w, http.StatusOK, resp)
}
