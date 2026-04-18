package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ClassOption is one class identity we'd accept booking for a given day.
// Days may list multiple options in priority order (index 0 = most preferred).
type ClassOption struct {
	Time     string `yaml:"time"`               // "HH:MM" in config TZ
	Category string `yaml:"category,omitempty"` // substring of Arbox box_categories.name; optional
}

// DayConfig describes what we want to book on a given weekday.
//
// Two ways to write it:
//   - Shorthand: `time: "08:30"` (optionally `category:`) — a single option.
//   - Full:      `options: [{time: "08:30", category: "Hall B"}, ...]` —
//                ordered list, first entry is the preferred class.
//
// A day is "actionable" only if Enabled=true AND has at least one resolvable
// option (via shorthand, options list, or the config-level default_time).
type DayConfig struct {
	Enabled bool `yaml:"enabled"`

	// Shorthand for a single-option day. Mutually exclusive with Options.
	Time     string `yaml:"time,omitempty"`
	Category string `yaml:"category,omitempty"`

	// Full form: ordered priority list. Index 0 is highest priority.
	Options []ClassOption `yaml:"options,omitempty"`
}

// CategoryFilter is the global include/exclude used when an option doesn't
// specify its own `category`. Substring match, case-insensitive.
type CategoryFilter struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

type Config struct {
	Timezone       string               `yaml:"timezone"`
	DefaultTime    string               `yaml:"default_time,omitempty"`
	CategoryFilter CategoryFilter       `yaml:"category_filter,omitempty"`
	Days           map[string]DayConfig `yaml:"days"`

	loc *time.Location
}

var validDays = []string{
	"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday",
}

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Load reads and parses a YAML config file. Call Validate() for semantic checks.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &c, nil
}

// Validate performs semantic checks: known day keys, valid HH:MM times, TZ
// resolvable, and that every enabled day resolves to at least one option.
func (c *Config) Validate() error {
	var errs []string

	if strings.TrimSpace(c.Timezone) == "" {
		errs = append(errs, "timezone is required")
	} else {
		loc, err := time.LoadLocation(c.Timezone)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid timezone %q: %v", c.Timezone, err))
		} else {
			c.loc = loc
		}
	}

	if c.DefaultTime != "" && !hhmmRe.MatchString(c.DefaultTime) {
		errs = append(errs, fmt.Sprintf("default_time %q is not HH:MM", c.DefaultTime))
	}

	known := map[string]bool{}
	for _, d := range validDays {
		known[d] = true
	}
	for key := range c.Days {
		if !known[strings.ToLower(key)] {
			errs = append(errs, fmt.Sprintf("unknown day key %q", key))
		}
	}

	for _, day := range validDays {
		d, ok := c.Days[day]
		if !ok {
			continue
		}
		if d.Time != "" && len(d.Options) > 0 {
			errs = append(errs, fmt.Sprintf("%s: set either `time:` OR `options:`, not both", day))
			continue
		}
		if d.Time != "" && !hhmmRe.MatchString(d.Time) {
			errs = append(errs, fmt.Sprintf("%s: time %q is not HH:MM", day, d.Time))
		}
		for i, opt := range d.Options {
			if opt.Time == "" {
				errs = append(errs, fmt.Sprintf("%s: options[%d] is missing time", day, i))
			} else if !hhmmRe.MatchString(opt.Time) {
				errs = append(errs, fmt.Sprintf("%s: options[%d].time %q is not HH:MM", day, i, opt.Time))
			}
		}
		if d.Enabled {
			if d.Time == "" && len(d.Options) == 0 && c.DefaultTime == "" {
				errs = append(errs,
					fmt.Sprintf("%s is enabled but has no time/options and no default_time is set", day))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config invalid:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Location returns the parsed timezone. Validate must have succeeded.
func (c *Config) Location() *time.Location {
	if c.loc == nil {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			c.loc = loc
		}
	}
	return c.loc
}

// OptionsFor returns the ordered priority list of ClassOptions for a day, or
// nil if the day is disabled/missing/unresolvable. A single-option day
// (shorthand `time:`) is returned as a one-element slice. The default_time
// fallback is applied when neither `time:` nor `options:` is set.
//
// The returned slice is always in priority order; index 0 is most preferred.
func (c *Config) OptionsFor(day time.Weekday) []ClassOption {
	key := strings.ToLower(day.String())
	d, ok := c.Days[key]
	if !ok || !d.Enabled {
		return nil
	}
	if len(d.Options) > 0 {
		out := make([]ClassOption, len(d.Options))
		copy(out, d.Options)
		return out
	}
	t := d.Time
	if t == "" {
		t = c.DefaultTime
	}
	if t == "" {
		return nil
	}
	return []ClassOption{{Time: t, Category: d.Category}}
}

// TimeFor is a compatibility shim for the old single-option API; it returns
// the top-priority option's time. Prefer OptionsFor for new code.
func (c *Config) TimeFor(day time.Weekday) (string, bool) {
	opts := c.OptionsFor(day)
	if len(opts) == 0 {
		return "", false
	}
	return opts[0].Time, true
}
