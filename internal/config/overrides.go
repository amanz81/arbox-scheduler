package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// OneTimeOverrides is a map of "YYYY-MM-DD" date strings to a DayConfig that
// REPLACES the weekday plan for that specific calendar date. Used to express
// "don't book this Tuesday" (enabled: false) or "just this Sunday book Hall A"
// without mutating the recurring `days:` plan.
//
// The zero value is a no-op: if the field is nil or empty, OptionsForDate
// falls through to the regular weekday plan.
//
// File format on disk (see MergeOverridesFromFile):
//
//	version: 1
//	overrides:
//	  "2026-04-26":
//	    enabled: true
//	    options:
//	      - { time: "08:00", category: "Hall A" }
//	  "2026-04-28":
//	    enabled: false
type OneTimeOverrides map[string]DayConfig

// dateKey returns the YYYY-MM-DD key for a date in the config's timezone.
// Using the config location (not UTC) is deliberate: an override written by
// the user as "for Sunday 26 Apr" must match Sunday 26 Apr in their gym's
// timezone, not UTC — otherwise a late-night Asia/Jerusalem window straddling
// UTC midnight would match the wrong day.
func dateKey(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02")
}

// MergeOverridesFromFile reads YYYY-MM-DD keyed DayConfig overrides from the
// given YAML file and merges them into c.OneTimeOverrides. Missing file is a
// no-op (makes first-run friendly). Expired entries (date < today in cfg
// timezone) are silently skipped — they'd never match anyway, and dropping
// them on read keeps the in-memory map small without needing a GC pass.
func (c *Config) MergeOverridesFromFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read overrides %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	var wrap struct {
		Version   int                  `yaml:"version"`
		Overrides map[string]DayConfig `yaml:"overrides"`
	}
	if err := yaml.Unmarshal(b, &wrap); err != nil {
		return fmt.Errorf("parse overrides %s: %w", path, err)
	}
	if len(wrap.Overrides) == 0 {
		return nil
	}

	loc := c.Location()
	today := dateKey(time.Now(), loc)
	if c.OneTimeOverrides == nil {
		c.OneTimeOverrides = OneTimeOverrides{}
	}
	for k, v := range wrap.Overrides {
		k = strings.TrimSpace(k)
		// Validate shape: YYYY-MM-DD.
		if _, err := time.ParseInLocation("2006-01-02", k, loc); err != nil {
			return fmt.Errorf("overrides key %q is not YYYY-MM-DD", k)
		}
		// Drop expired entries. Comparing strings (ISO dates are
		// lexicographically ordered) is faster than re-parsing and keeps
		// the map small on long-running daemons.
		if k < today {
			continue
		}
		c.OneTimeOverrides[k] = v
	}
	return nil
}

// OptionsForDate returns the scheduled class options for a specific calendar
// date. Override precedence (highest first):
//
//  1. OneTimeOverrides[dateKey(d)] — per-date user override (disables day if
//     override.Enabled == false)
//  2. c.OptionsFor(d.Weekday()) — recurring weekday plan from Days
//
// Returns nil for rest days (override disabled, or weekday disabled/unmapped,
// or no default time resolves).
func (c *Config) OptionsForDate(d time.Time) []ClassOption {
	loc := c.Location()
	if c.OneTimeOverrides != nil {
		key := dateKey(d, loc)
		if override, ok := c.OneTimeOverrides[key]; ok {
			if !override.Enabled {
				return nil
			}
			if len(override.Options) > 0 {
				out := make([]ClassOption, len(override.Options))
				copy(out, override.Options)
				return out
			}
			t := override.Time
			if t == "" {
				t = c.DefaultTime
			}
			if t == "" {
				return nil
			}
			return []ClassOption{{Time: t, Category: override.Category}}
		}
	}
	return c.OptionsFor(d.Weekday())
}

// SortedOverrideKeys returns override keys in ISO date order. Useful for
// deterministic YAML output and status reports.
func (c *Config) SortedOverrideKeys() []string {
	keys := make([]string, 0, len(c.OneTimeOverrides))
	for k := range c.OneTimeOverrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
