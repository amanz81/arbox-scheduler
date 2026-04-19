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

// UnmarshalYAML accepts either a YAML sequence of strings or a single
// flow-style string like `Hall A, Hall B` (common copy-paste mistake).
func (cf *CategoryFilter) UnmarshalYAML(n *yaml.Node) error {
	*cf = CategoryFilter{}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		kn := n.Content[i]
		vn := n.Content[i+1]
		if kn.Kind != yaml.ScalarNode {
			continue
		}
		switch kn.Value {
		case "include":
			cf.Include = flattenYAMLStringList(vn)
		case "exclude":
			cf.Exclude = flattenYAMLStringList(vn)
		}
	}
	return nil
}

func flattenYAMLStringList(node *yaml.Node) []string {
	if node == nil || node.Kind == 0 {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		s := strings.TrimSpace(node.Value)
		if s == "" {
			return nil
		}
		if !strings.Contains(s, ",") {
			return []string{s}
		}
		var out []string
		for _, p := range strings.Split(s, ",") {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		return out
	case yaml.SequenceNode:
		var out []string
		for _, c := range node.Content {
			if c.Kind == yaml.ScalarNode {
				if t := strings.TrimSpace(c.Value); t != "" {
					out = append(out, t)
				}
			}
		}
		return out
	default:
		return nil
	}
}

type Config struct {
	Timezone       string               `yaml:"timezone"`
	DefaultTime    string               `yaml:"default_time,omitempty"`
	CategoryFilter CategoryFilter       `yaml:"category_filter,omitempty"`
	Days           map[string]DayConfig `yaml:"days"`

	// Gym is an optional case-insensitive substring used to pick the right
	// box+location when /api/v2/boxes/locations returns more than one (e.g.
	// member of multiple gyms). Matched against box name and location name.
	Gym string `yaml:"gym,omitempty"`

	loc *time.Location
}

var validDays = []string{
	"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday",
}

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Load reads and parses a YAML config file. Call Validate() for semantic checks.
//
// Environment overrides (applied AFTER YAML parse, so they win):
//   ARBOX_GYM       — overrides `gym:` (substring match for multi-gym accounts)
//   ARBOX_TIMEZONE  — overrides `timezone:` (e.g. "Europe/Berlin")
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if env := strings.TrimSpace(os.Getenv("ARBOX_GYM")); env != "" {
		c.Gym = env
	}
	if env := strings.TrimSpace(os.Getenv("ARBOX_TIMEZONE")); env != "" {
		c.Timezone = env
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
