package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
)

// TestScheduleCache_DedupsRepeatedFetches verifies that two cached calls for
// the same (day, locID) only hit the upstream API once.
func TestScheduleCache_DedupsRepeatedFetches(t *testing.T) {
	resetScheduleCache()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v2/schedule/betweenDates") {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := arboxapi.New(srv.URL)
	c.Token = "tok"
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	day := time.Date(2026, 5, 3, 12, 0, 0, 0, loc)

	for i := 0; i < 3; i++ {
		if _, err := getScheduleDayCached(context.Background(), c, day, 1575); err != nil {
			t.Fatal(err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream fetches: got %d want 1", got)
	}
}

// TestScheduleCache_DifferentDaysAreSeparate verifies the cache key includes
// both day and locID.
func TestScheduleCache_DifferentDaysAreSeparate(t *testing.T) {
	resetScheduleCache()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	c := arboxapi.New(srv.URL)
	c.Token = "tok"
	loc, _ := time.LoadLocation("Asia/Jerusalem")

	if _, err := getScheduleDayCached(context.Background(), c, time.Date(2026, 5, 3, 0, 0, 0, 0, loc), 1575); err != nil {
		t.Fatal(err)
	}
	if _, err := getScheduleDayCached(context.Background(), c, time.Date(2026, 5, 4, 0, 0, 0, 0, loc), 1575); err != nil {
		t.Fatal(err)
	}
	if _, err := getScheduleDayCached(context.Background(), c, time.Date(2026, 5, 4, 0, 0, 0, 0, loc), 9999); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("upstream fetches: got %d want 3", got)
	}
}

// resetScheduleCache clears the package-level cache between tests.
func resetScheduleCache() {
	scheduleCacheMu.Lock()
	defer scheduleCacheMu.Unlock()
	scheduleCache = map[scheduleCacheKey]scheduleCacheEntry{}
}
