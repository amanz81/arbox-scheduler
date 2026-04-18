package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
)

// parseMorningArgs accepts up to two args in any order:
//   - "HH-HH" (e.g. "8-10", "06-12") — start/end hour window
//   - "N"    (e.g. "3", "7")         — days to look ahead (1..30)
// Defaults are returned when an arg is missing.
func parseMorningArgs(args []string, defStart, defEnd, defDays int) (startH, endH, days int, err error) {
	startH, endH, days = defStart, defEnd, defDays
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.Contains(a, "-") {
			parts := strings.SplitN(a, "-", 2)
			s, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			e, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 != nil || err2 != nil {
				return 0, 0, 0, fmt.Errorf("bad time range %q (want HH-HH)", a)
			}
			if s < 0 || s > 23 || e < 1 || e > 24 || s >= e {
				return 0, 0, 0, fmt.Errorf("bad time range %q (need 0<=start<end<=24)", a)
			}
			startH, endH = s, e
			continue
		}
		n, errN := strconv.Atoi(a)
		if errN != nil {
			return 0, 0, 0, fmt.Errorf("bad arg %q (want HH-HH or N)", a)
		}
		if n < 1 || n > 30 {
			return 0, 0, 0, fmt.Errorf("days out of range: %d (1..30)", n)
		}
		days = n
	}
	return startH, endH, days, nil
}

// buildMorningReport returns a per-day listing of classes whose start time is
// in [startH:00, endH:00). No category_filter is applied — this is a raw
// "what's actually on the schedule" view, with your booking flag.
func buildMorningReport(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, startH, endH, days int) (string, error) {
	loc, now, windowStart, allBy, err := fetchScheduleWindow(ctx, c, client, locID, days)
	if err != nil {
		return "", err
	}
	startMin := startH * 60
	endMin := endH * 60

	var b strings.Builder
	fmt.Fprintf(&b, "Live Arbox classes %02d:00–%02d:00 (next %d days, %s):\n", startH, endH, days, c.Timezone)

	any := false
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		var rows []arboxapi.Class
		for _, cl := range allBy[key] {
			t := hhmm(cl.Time)
			m, ok := parseHHMMMinutes(t)
			if !ok {
				continue
			}
			if m < startMin || m >= endMin {
				continue
			}
			// Drop classes already started today; keep all on later days.
			if i == 0 {
				when, errw := classStartsAt(cl, key, loc)
				if errw == nil && !when.After(now) {
					continue
				}
			}
			rows = append(rows, cl)
		}
		if len(rows) == 0 {
			continue
		}
		sort.SliceStable(rows, func(i, j int) bool {
			return hhmm(rows[i].Time) < hhmm(rows[j].Time)
		})
		any = true
		fmt.Fprintf(&b, "\n%s %s:\n", d.Weekday().String()[:3], key)
		for _, cl := range rows {
			you := cl.YouStatus()
			if you == "" {
				you = "-"
			}
			cap := cl.MaxUsers
			if cap <= 0 {
				cap = cl.Registered + cl.Free
			}
			marker := ""
			if you == "BOOKED" {
				marker = "  ← your booking"
			} else if you == "WAITLIST" {
				marker = "  ← on waitlist"
			}
			fmt.Fprintf(&b, "  %s  %s  %d/%d (free %d, wl %d)  you %s  id %d%s\n",
				hhmm(cl.Time),
				cl.ResolvedCategoryName(),
				cl.Registered, cap, cl.Free, cl.StandBy,
				you, cl.ID, marker,
			)
		}
	}
	if !any {
		fmt.Fprintf(&b, "(no classes returned in this window for the next %d days)\n", days)
	}
	return b.String(), nil
}
