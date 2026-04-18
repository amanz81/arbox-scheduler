// Package schedule computes planned class options and the exact moment each
// booking window opens.
//
// Arbox booking rules:
//   - Most days: window opens 24h before class start.
//   - Sunday:    window opens 48h before class start.
//
// All arithmetic is done in the configured timezone (e.g. Asia/Jerusalem) using
// time.Location so DST transitions are handled correctly.
package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/config"
)

// PlannedOption is one actionable class wish for a specific future date.
// A single day with multiple priorities produces multiple PlannedOptions that
// share the same Date but differ in Priority (0 = most preferred).
type PlannedOption struct {
	ClassStart time.Time   // local time, in cfg.Location()
	WindowOpen time.Time   // local time, in cfg.Location()
	Weekday    time.Weekday
	Priority   int         // 0-based; 0 = most preferred for this day
	Time       string      // "HH:MM" copied from config, for display
	Category   string      // optional substring filter copied from config
}

// SundayBookingLead is the Sunday-specific lead time.
const SundayBookingLead = 48 * time.Hour

// DefaultBookingLead is the lead time for every non-Sunday class.
const DefaultBookingLead = 24 * time.Hour

// leadFor returns the booking-window lead time for a class on the given weekday.
func leadFor(day time.Weekday) time.Duration {
	if day == time.Sunday {
		return SundayBookingLead
	}
	return DefaultBookingLead
}

// NextOptions returns planned class options for the `days` calendar days
// starting on `from`, expanded from each day's priority list in cfg. A day
// with N options contributes N entries (same date, priorities 0..N-1).
// Entries are sorted by ClassStart asc, then Priority asc.
//
// Disabled days and calendar days whose class_start <= `from` are skipped.
func NextOptions(cfg *config.Config, from time.Time, days int) ([]PlannedOption, error) {
	loc := cfg.Location()
	if loc == nil {
		return nil, fmt.Errorf("config has no timezone resolved; call Validate first")
	}
	from = from.In(loc)

	var out []PlannedOption
	cursor := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < days; i++ {
		d := cursor.AddDate(0, 0, i)
		opts := cfg.OptionsFor(d.Weekday())
		if len(opts) == 0 {
			continue
		}
		for idx, opt := range opts {
			hour, minute, err := parseHHMM(opt.Time)
			if err != nil {
				return nil, fmt.Errorf("%s option[%d]: %w", d.Weekday(), idx, err)
			}
			classStart := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, loc)
			if !classStart.After(from) {
				continue
			}
			out = append(out, PlannedOption{
				ClassStart: classStart,
				WindowOpen: classStart.Add(-leadFor(d.Weekday())),
				Weekday:    d.Weekday(),
				Priority:   idx,
				Time:       opt.Time,
				Category:   opt.Category,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ClassStart.Equal(out[j].ClassStart) {
			return out[i].ClassStart.Before(out[j].ClassStart)
		}
		return out[i].Priority < out[j].Priority
	})
	return out, nil
}

func parseHHMM(s string) (hour, minute int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid time %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return h, m, nil
}
