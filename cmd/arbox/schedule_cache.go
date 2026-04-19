package main

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
)

// scheduleCacheTTL controls how long a single (day, locations_box_id) Arbox
// fetch is reused. Telegram queries (/status, /morning, /evening, /selftest)
// hit the same days repeatedly, often within seconds of each other; without a
// cache, three /status sends in 30s = 21 GETs. With this TTL, the same window
// = 7 GETs (one per day, then reused).
//
// The booker DOES NOT go through the cache — it always fetches fresh because
// `free` counts can change second-by-second around WindowOpen.
const scheduleCacheTTL = 30 * time.Second

type scheduleCacheKey struct {
	day   string // YYYY-MM-DD (in config TZ)
	locID int
}

type scheduleCacheEntry struct {
	classes []arboxapi.Class
	fetched time.Time
}

var (
	scheduleCacheMu sync.Mutex
	scheduleCache   = map[scheduleCacheKey]scheduleCacheEntry{}
)

// getScheduleDayCached returns cached classes for (day, locID) if the entry is
// younger than scheduleCacheTTL; otherwise it fetches fresh and caches.
//
// Cache is per-process and lost on restart. That's intentional — we never want
// to serve a stale cache on the booker path, and Telegram queries don't need
// strong consistency.
func getScheduleDayCached(ctx context.Context, client *arboxapi.Client, day time.Time, locID int) ([]arboxapi.Class, error) {
	key := scheduleCacheKey{day: day.Format("2006-01-02"), locID: locID}

	scheduleCacheMu.Lock()
	if e, ok := scheduleCache[key]; ok && time.Since(e.fetched) < scheduleCacheTTL {
		out := e.classes
		scheduleCacheMu.Unlock()
		return out, nil
	}
	scheduleCacheMu.Unlock()

	classes, err := client.GetScheduleDay(ctx, day, locID)
	if err != nil {
		return nil, err
	}

	scheduleCacheMu.Lock()
	scheduleCache[key] = scheduleCacheEntry{classes: classes, fetched: time.Now()}
	pruneScheduleCacheLocked()
	scheduleCacheMu.Unlock()
	return classes, nil
}

// pruneScheduleCacheLocked drops entries older than 5×TTL so the map doesn't
// grow forever. Caller must hold scheduleCacheMu.
func pruneScheduleCacheLocked() {
	if len(scheduleCache) < 64 {
		return
	}
	cutoff := time.Now().Add(-5 * scheduleCacheTTL)
	for k, e := range scheduleCache {
		if e.fetched.Before(cutoff) {
			delete(scheduleCache, k)
		}
	}
}

// scheduleCacheStats reports current entries and TTL — used by /selftest so
// the user can verify caching is working.
func scheduleCacheStats() (entries int, ttl time.Duration) {
	scheduleCacheMu.Lock()
	defer scheduleCacheMu.Unlock()
	return len(scheduleCache), scheduleCacheTTL
}

// pulserCounters wraps strconv to avoid an unused-import flagger when
// scheduleCacheTTL constants change shape later.
var _ = strconv.Itoa
