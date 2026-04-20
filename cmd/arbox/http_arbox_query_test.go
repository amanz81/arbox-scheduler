package main

// Tests for the /api/v1/arbox/query passthrough. Focus on the safety
// posture (method + path validation, blocklist) because the upstream
// call itself is just client.Raw which has its own coverage.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestArboxQuery_ReadTokenIs403(t *testing.T) {
	srv, _ := newTestServer(t, "read", "admin", nil)
	resp := post(t, srv.URL+"/api/v1/arbox/query", "read", map[string]any{
		"method": "GET",
		"path":   "/api/v2/user/feed",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("read token must be rejected, got %d", resp.StatusCode)
	}
}

func TestArboxQuery_NoAuthIs401(t *testing.T) {
	srv, _ := newTestServer(t, "read", "admin", nil)
	resp := post(t, srv.URL+"/api/v1/arbox/query", "", map[string]any{
		"method": "GET",
		"path":   "/api/v2/user/feed",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token must be 401, got %d", resp.StatusCode)
	}
}

func TestArboxQuery_BadPath_Rejected(t *testing.T) {
	srv, _ := newTestServer(t, "read", "admin", nil)
	for _, p := range []string{
		"",                      // empty
		"/",                     // not in /api/v2
		"/api/v1/status",        // our own API — must not self-call
		"https://evil.com/xyz",  // absolute URL, cross-host
		"/api/v2/../../etc",     // path traversal attempt
		"/API/V2/user/feed",     // wrong case
	} {
		resp := post(t, srv.URL+"/api/v1/arbox/query", "admin", map[string]any{
			"method": "GET",
			"path":   p,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("path %q should be 400, got %d", p, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestArboxQuery_AuthPathBlocked(t *testing.T) {
	srv, _ := newTestServer(t, "read", "admin", nil)
	for _, p := range []string{
		"/api/v2/user/login",
		"/api/v2/user/logout",
		"/api/v2/user/refreshToken",
		"/api/v2/user/recoverPassword",
	} {
		resp := post(t, srv.URL+"/api/v1/arbox/query", "admin", map[string]any{
			"method": "POST",
			"path":   p,
			"body":   map[string]any{"email": "x"},
		})
		// Blocklist comes after path regex, so these are 403 (semantically
		// "authz refused" rather than 400 "invalid shape").
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("blocklisted path %q should be 403, got %d", p, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestArboxQuery_BadMethodRejected(t *testing.T) {
	srv, _ := newTestServer(t, "read", "admin", nil)
	resp := post(t, srv.URL+"/api/v1/arbox/query", "admin", map[string]any{
		"method": "TRACE",
		"path":   "/api/v2/user/feed",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("TRACE method should be 400, got %d", resp.StatusCode)
	}
}

func TestArboxQuery_ForwardsGETWithAuth_AndReturnsJSON(t *testing.T) {
	// Custom upstream that replies to /api/v2/user/feed with a known JSON.
	srv, _ := newTestServer(t, "read", "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/v2/user/feed") {
			_, _ = w.Write([]byte(`{"data":[{"id":1,"type":"notif"}]}`))
			return
		}
		http.NotFound(w, r)
	}))

	resp := post(t, srv.URL+"/api/v1/arbox/query", "admin", map[string]any{
		"method": "GET",
		"path":   "/api/v2/user/feed",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["status"] != float64(200) {
		t.Errorf("status=%v want 200", out["status"])
	}
	if out["body_is_json"] != true {
		t.Errorf("body_is_json must be true for JSON upstream reply")
	}
	if out["path"] != "/api/v2/user/feed" {
		t.Errorf("path echo wrong: %v", out["path"])
	}
}

func TestArboxQuery_NonJSONUpstreamIsBase64Wrapped(t *testing.T) {
	srv, _ := newTestServer(t, "read", "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/v2/healthz") {
			// Arbox occasionally returns HTML when Cloudflare interjects.
			// We want the LLM to see that honestly, not a decode error.
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!DOCTYPE html><html>challenge</html>"))
			return
		}
		http.NotFound(w, r)
	}))

	resp := post(t, srv.URL+"/api/v1/arbox/query", "admin", map[string]any{
		"method": "GET",
		"path":   "/api/v2/healthz",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["body_is_json"] != false {
		t.Errorf("HTML upstream must set body_is_json=false; got %v", out["body_is_json"])
	}
	b64, _ := out["body"].(string)
	if b64 == "" {
		t.Fatalf("body must be base64 string when not JSON; got %v", out["body"])
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("body is not valid base64: %v", err)
	}
	if !strings.Contains(string(decoded), "challenge") {
		t.Errorf("decoded body should contain the HTML marker; got %q", decoded)
	}
}

func TestArboxQuery_ForwardsPOSTBody(t *testing.T) {
	var gotBody []byte
	srv, _ := newTestServer(t, "read", "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/v2/notifications/markRead") {
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.NotFound(w, r)
	}))

	_ = post(t, srv.URL+"/api/v1/arbox/query", "admin", map[string]any{
		"method": "POST",
		"path":   "/api/v2/notifications/markRead",
		"body":   map[string]any{"ids": []int{1, 2, 3}},
	}).Body.Close()

	var upstream map[string]any
	if err := json.Unmarshal(gotBody, &upstream); err != nil {
		t.Fatalf("upstream body was not JSON: %v (%s)", err, gotBody)
	}
	ids, ok := upstream["ids"].([]any)
	if !ok || len(ids) != 3 {
		t.Errorf("upstream didn't receive ids: %v", upstream)
	}
}
