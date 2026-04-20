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
//   - "week" / "w"                   — alias for 7 days
// Defaults: defStart..defEnd over 1 day.
func parseMorningArgs(args []string, defStart, defEnd, defDays int) (startH, endH, days int, err error) {
	startH, endH, days = defStart, defEnd, 1
	_ = defDays
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		switch strings.ToLower(a) {
		case "week", "w":
			days = 7
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

// buildMorningReport — compatibility wrapper kept for tests/callers; see
// buildClassWindowReport.
func buildMorningReport(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, startH, endH, days int) (string, error) {
	return buildClassWindowReport(ctx, c, client, locID, startH, endH, days)
}

// buildClassWindowReport returns a compact per-day listing of classes whose
// start time is in [startH:00, endH:00). The global category_filter is applied
// so the gym's Open Box / kids / etc. are excluded.
func buildClassWindowReport(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, startH, endH, days int) (string, error) {
	loc, now, windowStart, allBy, err := fetchScheduleWindow(ctx, c, client, locID, days)
	if err != nil {
		return "", err
	}
	startMin := startH * 60
	endMin := endH * 60

	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s (%s)\n", now.Format("Mon 02 Jan 15:04 MST"), c.Timezone)
	if ps, err := readPauseState(); err == nil {
		if tag := ps.Summary(now, loc); tag != "" {
			fmt.Fprintf(&b, "%s\n", tag)
		}
	}
	fmt.Fprintf(&b, "%02d:00–%02d:00, next %d day(s), filter applied.\n", startH, endH, days)

	any := false
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		var rows []arboxapi.Class
		for _, cl := range allBy[key] {
			if !classPassesGlobalFilter(cl.ResolvedCategoryName(), c.CategoryFilter) {
				continue
			}
			t := hhmm(cl.Time)
			m, ok := parseHHMMMinutes(t)
			if !ok {
				continue
			}
			if m < startMin || m >= endMin {
				continue
			}
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
		fmt.Fprintf(&b, "\n%s %s\n", d.Weekday().String()[:3], d.Format("02 Jan"))
		for _, cl := range rows {
			you := cl.YouStatus()
			tag := ""
			switch you {
			case "BOOKED":
				tag = " — BOOKED"
			case "WAITLIST":
				// YouStatusDetail adds "3/7" or "#3" when available.
				tag = " — " + cl.YouStatusDetail()
			default:
				tag = fmt.Sprintf(" — free %d", cl.Free)
			}
			fmt.Fprintf(&b, "  %s  %s%s  (id %d)\n",
				hhmm(cl.Time),
				cl.ResolvedCategoryName(),
				tag, cl.ID,
			)
		}
	}
	if !any {
		fmt.Fprintf(&b, "(no classes after now in this window)\n")
	}
	return b.String(), nil
}
