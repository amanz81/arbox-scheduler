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

	"github.com/lafofo-nivo/arbox-scheduler/internal/config"
)

// PlannedOption is one actionable class wish for a specific future date.
// Exactly one PlannedOption per scheduled calendar day — priority-list
// fallback (multiple options per day) was removed in favor of the explicit
// one-time override file (see config.OneTimeOverrides) for "just this
// Sunday book Hall A instead" use cases. Having waitlisted slot + booked
// fallback at the same time was confusing; now you get exactly what you
// picked or a clean "no match" log line.
type PlannedOption struct {
	ClassStart time.Time // local time, in cfg.Location()
	WindowOpen time.Time // local time, in cfg.Location()
	Weekday    time.Weekday
	Time       string // "HH:MM" copied from config, for display
	Category   string // optional substring filter copied from config
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

// NextOptions returns at most one PlannedOption per calendar day in the
// `days` window starting at `from`. Source of truth (in precedence order):
//
//  1. cfg.OneTimeOverrides[YYYY-MM-DD] — if set, replaces the weekday plan
//  2. cfg.Days[weekday]                 — the recurring weekly plan
//
// Both layers yield at most one ClassOption (enforced by Config.Validate),
// so the output is a flat list sorted by ClassStart ascending. Days that
// are disabled, unmapped, or whose class_start has already passed are
// skipped.
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
		opts := cfg.OptionsForDate(d)
		if len(opts) == 0 {
			continue
		}
		// Defensive: OptionsForDate returns at most one option per day.
		// If that contract is ever violated we'd silently book something
		// unexpected, so take only opts[0] explicitly.
		opt := opts[0]
		hour, minute, err := parseHHMM(opt.Time)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Weekday(), err)
		}
		classStart := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, loc)
		if !classStart.After(from) {
			continue
		}
		out = append(out, PlannedOption{
			ClassStart: classStart,
			WindowOpen: classStart.Add(-leadFor(d.Weekday())),
			Weekday:    d.Weekday(),
			Time:       opt.Time,
			Category:   opt.Category,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ClassStart.Before(out[j].ClassStart)
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
