package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
)

// setupCandidate is one selectable row in Telegram /setup (one Arbox class
// pattern: time + full category name for YAML substring matching).
type setupCandidate struct {
	Time     string `json:"time"`
	Category string `json:"category"`
	Label    string `json:"label"`
}

var setupWeekdayOrder = []string{
	"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday",
}

var dayKeyToWeekday = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

// buildSetupCandidates walks the next `horizonDays` calendar days, and for
// each weekday name picks the *first* calendar day in that window matching
// the weekday, loads real classes from Arbox, filters with the global
// category_filter, and de-dupes by time + category name.
func buildSetupCandidates(ctx context.Context, cfg *config.Config, client *arboxapi.Client, locID, horizonDays int) (map[string][]setupCandidate, error) {
	loc := cfg.Location()
	from := time.Now().In(loc)
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)

	out := make(map[string][]setupCandidate)
	for _, dayKey := range setupWeekdayOrder {
		want, ok := dayKeyToWeekday[dayKey]
		if !ok {
			continue
		}
		var target time.Time
		found := false
		for i := 0; i < horizonDays; i++ {
			d := start.AddDate(0, 0, i)
			if d.Weekday() == want {
				target = d
				found = true
				break
			}
		}
		if !found {
			continue
		}

		ctx2, cancel := context.WithTimeout(ctx, 25*time.Second)
		classes, err := client.GetScheduleDay(ctx2, target, locID)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dayKey, err)
		}

		seen := make(map[string]bool)
		var row []setupCandidate
		for _, cl := range classes {
			name := cl.ResolvedCategoryName()
			if !classPassesGlobalFilter(name, cfg.CategoryFilter) {
				continue
			}
			key := cl.Time + "\t" + strings.ToLower(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			// Strip the gym's noisy prefix so labels fit in narrow Telegram
			// buttons (e.g. "CrossFit- Hall A" -> "Hall A", "Crossfit Hall B"
			// -> "Hall B"). Shorter labels render fully on mobile.
			short := shortenCategoryForButton(name)
			row = append(row, setupCandidate{
				Time:     cl.Time,
				Category: name,
				Label:    fmt.Sprintf("%s %s", hhmm(cl.Time), truncateRunes(short, 28)),
			})
		}
		sort.Slice(row, func(i, j int) bool {
			if row[i].Time != row[j].Time {
				return row[i].Time < row[j].Time
			}
			return row[i].Category < row[j].Category
		})
		if len(row) > 0 {
			out[dayKey] = row
		}
	}
	return out, nil
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// shortenCategoryForButton drops common gym prefixes ("CrossFit- ", "Crossfit ")
// so the button text leads with the meaningful part (e.g. "Hall A").
func shortenCategoryForButton(name string) string {
	s := strings.TrimSpace(name)
	low := strings.ToLower(s)
	for _, p := range []string{"crossfit- ", "crossfit-", "crossfit "} {
		if strings.HasPrefix(low, p) {
			return strings.TrimSpace(s[len(p):])
		}
	}
	return s
}
