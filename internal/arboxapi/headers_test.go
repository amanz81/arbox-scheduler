package arboxapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func timeMustParseUTC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// TestClient_CloudflarePassingHeaders asserts that every request carries the
// browser-impersonation headers Cloudflare's default bot check looks at. A
// regression here would silently reintroduce the 403 "challenge HTML" we
// debugged in prod on 2026-04-20.
func TestClient_CloudflarePassingHeaders(t *testing.T) {
	var gotUA, gotAL, gotOrigin, gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAL = r.Header.Get("Accept-Language")
		gotOrigin = r.Header.Get("Origin")
		gotReferer = r.Header.Get("Referer")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.Token = "tok" // so applyCommonHeaders runs the authed path
	if _, err := c.GetSchedule(context.Background(), timeMustParseUTC("2026-04-19T00:00:00Z"), timeMustParseUTC("2026-04-19T00:00:00Z"), 1); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}

	if !strings.Contains(gotUA, "Mozilla/5.0") || !strings.Contains(gotUA, "Safari") {
		t.Errorf("UA should look like a desktop browser, got %q", gotUA)
	}
	if !strings.Contains(gotAL, "en") {
		t.Errorf("Accept-Language should include a locale, got %q", gotAL)
	}
	if gotOrigin != "https://app.arboxapp.com" {
		t.Errorf("Origin = %q, want the web-app origin", gotOrigin)
	}
	if gotReferer != "https://app.arboxapp.com/" {
		t.Errorf("Referer = %q, want the web-app root", gotReferer)
	}
	if strings.Contains(strings.ToLower(gotUA), "go-http-client") ||
		strings.Contains(strings.ToLower(gotUA), "arbox-scheduler") {
		t.Errorf("UA still exposes a programmatic signature: %q — Cloudflare will 403 us", gotUA)
	}
}
