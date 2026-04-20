package main

// Integration tests for the HTTP API. Each test wires a real *apiServer
// (with a fake Arbox upstream via httptest.NewServer) into an in-process
// httptest.NewServer so we exercise auth + rate limit + handler + audit
// log end-to-end.
//
// All on-disk paths (audit log, attempts, env, pause, user_plan) are
// redirected to t.TempDir() so the user's real /data is never touched.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
)

// ----- shared fixtures ---------------------------------------------------

func newTestServer(t *testing.T, readTok, adminTok string, upstream http.Handler) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("ARBOX_AUDIT_LOG", dir+"/audit.jsonl")
	t.Setenv("ARBOX_BOOKING_ATTEMPTS", dir+"/attempts.json")
	t.Setenv("ARBOX_ENV_FILE", dir+"/.env")
	t.Setenv("ARBOX_PAUSE_STATE", dir+"/pause.json")
	t.Setenv("ARBOX_USER_PLAN", dir+"/user_plan.yaml")
	t.Setenv("ARBOX_BOX_ID", "1130")
	t.Setenv("ARBOX_LOCATIONS_BOX_ID", "1575")
	t.Setenv("ARBOX_MEMBERSHIP_USER_ID", "99")

	var bookCalls atomic.Int32
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Default fake handlers; tests can override via the upstream
		// handler arg below.
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v2/scheduleUser/insert"):
			bookCalls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"id":777}}`))
		case strings.HasSuffix(r.URL.Path, "/api/v2/scheduleUser/cancel"):
			_, _ = w.Write([]byte(`{"data":{"id":777}}`))
		case strings.HasSuffix(r.URL.Path, "/api/v2/scheduleStandBy/insert"):
			_, _ = w.Write([]byte(`{"data":{"id":777}}`))
		case strings.HasSuffix(r.URL.Path, "/api/v2/scheduleStandBy/delete"):
			_, _ = w.Write([]byte(`{"data":{"id":777}}`))
		case strings.HasSuffix(r.URL.Path, "/api/v2/schedule/betweenDates"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/api/v2/boxes/locations"):
			_, _ = w.Write([]byte(`{"data":[{"id":1130,"name":"Test Gym","locations_box":[{"id":1575,"name":""}]}]}`))
		case strings.Contains(r.URL.Path, "/api/v2/boxes/") && strings.HasSuffix(r.URL.Path, "/memberships/1"):
			_, _ = w.Write([]byte(`{"data":[{"id":99,"user_fk":1,"box_fk":1130,"active":1,"membership_types":{"name":"plan"}}]}`))
		default:
			if upstream != nil {
				upstream.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstreamSrv.Close)

	client := arboxapi.New(upstreamSrv.URL)
	client.Token = "tok"

	cfg := &config.Config{
		Timezone:    "Asia/Jerusalem",
		DefaultTime: "08:30",
		Days: map[string]config.DayConfig{
			"sunday":    {Enabled: true, Options: []config.ClassOption{{Time: "08:00", Category: "Hall A"}}},
			"monday":    {Enabled: false},
			"tuesday":   {Enabled: false},
			"wednesday": {Enabled: false},
			"thursday":  {Enabled: false},
			"friday":    {Enabled: false},
			"saturday":  {Enabled: false},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	srv := &apiServer{
		cfgReload:     func() (*config.Config, error) { return cfg, nil },
		client:        client,
		locID:         1575,
		lookaheadDays: 7,
		auth:          newAuthMiddleware(readTok, adminTok),
		limiter:       newAPIRateLimiter(),
	}
	apiSrv := httptest.NewServer(srv.routes())
	t.Cleanup(apiSrv.Close)
	return apiSrv, &bookCalls
}

func get(t *testing.T, url, tok string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func post(t *testing.T, url, tok string, body any) *http.Response {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, r *http.Response) []byte {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ----- tests --------------------------------------------------------------

func TestHTTPAPI_HealthzNoAuth(t *testing.T) {
	srv, _ := newTestServer(t, "rtok", "atok", nil)
	resp := get(t, srv.URL+"/api/v1/healthz", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: want 200 got %d (body=%s)", resp.StatusCode, readBody(t, resp))
	}
	var out map[string]any
	if err := json.Unmarshal(readBody(t, resp), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("healthz: ok!=true: %v", out)
	}
}

func TestHTTPAPI_OpenAPINoAuth(t *testing.T) {
	srv, _ := newTestServer(t, "rtok", "atok", nil)
	resp := get(t, srv.URL+"/api/v1/openapi.json", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openapi: want 200 got %d", resp.StatusCode)
	}
	var spec map[string]any
	if err := json.Unmarshal(readBody(t, resp), &spec); err != nil {
		t.Fatalf("openapi not valid JSON: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("openapi missing paths: %v", spec)
	}
	for _, want := range []string{"/api/v1/version", "/api/v1/status", "/api/v1/book", "/api/v1/pause"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("openapi.paths missing %q", want)
		}
	}
}

func TestHTTPAPI_VersionRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t, "rtok", "atok", nil)
	resp := get(t, srv.URL+"/api/v1/version", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/version no token: want 401 got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("missing WWW-Authenticate: %q", got)
	}
	resp2 := get(t, srv.URL+"/api/v1/version", "rtok")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/version read-token: want 200 got %d (body=%s)", resp2.StatusCode, readBody(t, resp2))
	}
}

func TestHTTPAPI_ReadTokenForbiddenOnAdmin(t *testing.T) {
	srv, _ := newTestServer(t, "rtok", "atok", nil)
	resp := post(t, srv.URL+"/api/v1/book", "rtok", map[string]int{"schedule_id": 42})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("/book read-token: want 403 got %d", resp.StatusCode)
	}
}

func TestHTTPAPI_BookDryRunByDefault(t *testing.T) {
	srv, bookCalls := newTestServer(t, "rtok", "atok", nil)
	resp := post(t, srv.URL+"/api/v1/book", "atok", map[string]int{"schedule_id": 42})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/book dry-run: want 200 got %d (body=%s)", resp.StatusCode, readBody(t, resp))
	}
	var out map[string]any
	if err := json.Unmarshal(readBody(t, resp), &out); err != nil {
		t.Fatal(err)
	}
	if out["actual_send"] != false {
		t.Fatalf("/book dry-run: actual_send must be false: %v", out)
	}
	if got := bookCalls.Load(); got != 0 {
		t.Fatalf("/book dry-run must not call upstream, got %d calls", got)
	}
}

func TestHTTPAPI_BookConfirmCallsUpstreamAndAudits(t *testing.T) {
	srv, bookCalls := newTestServer(t, "rtok", "atok", nil)
	resp := post(t, srv.URL+"/api/v1/book?confirm=1", "atok", map[string]int{"schedule_id": 42})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/book confirm: want 200 got %d (body=%s)", resp.StatusCode, readBody(t, resp))
	}
	if got := bookCalls.Load(); got != 1 {
		t.Fatalf("/book confirm: want 1 upstream call, got %d", got)
	}
	auditResp := get(t, srv.URL+"/api/v1/audit?limit=5", "atok")
	if auditResp.StatusCode != http.StatusOK {
		t.Fatalf("/audit: want 200 got %d", auditResp.StatusCode)
	}
	var out map[string][]map[string]any
	if err := json.Unmarshal(readBody(t, auditResp), &out); err != nil {
		t.Fatal(err)
	}
	if len(out["entries"]) == 0 {
		t.Fatalf("/audit: no entries returned")
	}
	first := out["entries"][0]
	if first["route"] != "/api/v1/book" {
		t.Fatalf("/audit entry route mismatch: %v", first)
	}
	if first["confirm"] != true {
		t.Fatalf("/audit entry not marked confirm: %v", first)
	}
	if first["token_kind"] != "admin" {
		t.Fatalf("/audit entry token_kind: %v", first["token_kind"])
	}
}

func TestHTTPAPI_RateLimit429(t *testing.T) {
	srv, _ := newTestServer(t, "rtok", "atok", nil)
	// Burst is 30. Fire 31 quick requests with the same token; the 31st
	// should be rejected.
	var lastStatus int
	for i := 0; i < 31; i++ {
		resp := get(t, srv.URL+"/api/v1/version", "rtok")
		lastStatus = resp.StatusCode
		_ = readBody(t, resp)
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Fatalf("31st request: want 429 got %d", lastStatus)
	}
	resp := get(t, srv.URL+"/api/v1/version", "rtok")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("32nd request: want 429 got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Errorf("missing Retry-After header")
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != "60" {
		t.Errorf("X-RateLimit-Limit: want 60 got %q", got)
	}
}

func TestHTTPAPI_PauseDryRunThenConfirm(t *testing.T) {
	srv, _ := newTestServer(t, "rtok", "atok", nil)

	resp := post(t, srv.URL+"/api/v1/pause", "atok", map[string]any{"duration": "1h"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/pause dry-run: want 200 got %d (body=%s)", resp.StatusCode, readBody(t, resp))
	}
	var dry map[string]any
	_ = json.Unmarshal(readBody(t, resp), &dry)
	if dry["actual_send"] != false {
		t.Fatalf("/pause dry-run: %v", dry)
	}

	resp2 := post(t, srv.URL+"/api/v1/pause?confirm=1", "atok", map[string]any{"duration": "1h", "reason": "test"})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/pause confirm: want 200 got %d (body=%s)", resp2.StatusCode, readBody(t, resp2))
	}
	ps, err := readPauseState()
	if err != nil {
		t.Fatal(err)
	}
	if !ps.IsActive(time.Now()) {
		t.Fatalf("pause state not active: %+v", ps)
	}
	if ps.Reason != "test" {
		t.Fatalf("pause reason: %q", ps.Reason)
	}
}

func TestHTTPAPI_PlanReturnsConfig(t *testing.T) {
	srv, _ := newTestServer(t, "rtok", "atok", nil)
	resp := get(t, srv.URL+"/api/v1/plan", "rtok")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/plan: want 200 got %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.Unmarshal(readBody(t, resp), &out); err != nil {
		t.Fatal(err)
	}
	if out["Timezone"] != "Asia/Jerusalem" {
		t.Errorf("/plan timezone: %v", out["Timezone"])
	}
}
