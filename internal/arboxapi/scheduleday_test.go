package arboxapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestGetScheduleDay_SendsUTCMidnight asserts that GetScheduleDay sends the
// calendar date as YYYY-MM-DDT00:00:00.000Z (UTC midnight). This matches the
// reference Arbox impl; sending IL midnight (= previous-day 21:00 UTC) caused
// off-by-one bugs where Sunday returned Saturday's classes.
func TestGetScheduleDay_SendsUTCMidnight(t *testing.T) {
	il, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatalf("load IL: %v", err)
	}

	var seen ScheduleParams
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v2/schedule/betweenDates") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &seen); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.Token = "t"

	// Local IL noon — must still produce 2026-04-19T00:00:00.000Z.
	day := time.Date(2026, 4, 19, 12, 0, 0, 0, il)
	if _, err := c.GetScheduleDay(context.Background(), day, 1575); err != nil {
		t.Fatalf("GetScheduleDay: %v", err)
	}

	want := "2026-04-19T00:00:00.000Z"
	if seen.From != want || seen.To != want {
		t.Errorf("from/to = %q/%q, want %q/%q", seen.From, seen.To, want, want)
	}
	if seen.LocationsBoxID != 1575 {
		t.Errorf("locations_box_id: %d", seen.LocationsBoxID)
	}
}
