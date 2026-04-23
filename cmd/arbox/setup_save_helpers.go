package main

// Two single-day save helpers used by the per-day inline keyboard buttons in
// /setup. They share the underlying picks → DayConfig building logic with
// handleSetupDone but commit only one weekday's entry — either to
// user_plan.yaml (persistent, every future {weekday}) or to
// one_time_overrides.yaml (one-time, this date only).
//
// These are separate from handleSetupDone because the "save one day" buttons
// are shortcuts for the common case where the user only wants to edit one
// weekday. /setupdone still commits the whole session across all days; use
// that for bulk edits.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/amanz81/arbox-scheduler/internal/config"
)

// savePersistentForDay commits s.Picks[dayKey] to user_plan.yaml without
// touching other weekdays. Returns the pretty summary line for the chat
// reply. The caller is expected to have validated dayKey and picked indices.
//
// Behaviour (mirrors buildDayMapFromSession for one weekday):
//   - picks empty  → rest day for that weekday (enabled:false in the overlay)
//   - picks non-nil → enabled:true + Options in tap order
//
// We load + rewrite the overlay in place (preserving other weekdays' entries)
// so this doesn't step on another save. File is 0o600, atomic rename.
func savePersistentForDay(cfgPath, dayKey string, s *setupSession) (string, error) {
	dayKey = strings.ToLower(strings.TrimSpace(dayKey))
	if _, ok := dayKeyToWeekday[dayKey]; !ok {
		return "", fmt.Errorf("unknown weekday %q", dayKey)
	}
	if s == nil || len(s.Candidates[dayKey]) == 0 {
		return "", fmt.Errorf("no candidates for %s in this setup session", dayKey)
	}

	newDay, err := buildDayConfigForKey(s, dayKey)
	if err != nil {
		return "", err
	}

	// Load existing overlay so we keep other weekdays' entries.
	overlayPath := userPlanOverlayPath()
	var wrap struct {
		Days map[string]config.DayConfig `yaml:"days"`
	}
	if existing, err := os.ReadFile(overlayPath); err == nil {
		_ = yaml.Unmarshal(existing, &wrap)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read overlay: %w", err)
	}
	if wrap.Days == nil {
		wrap.Days = map[string]config.DayConfig{}
	}
	wrap.Days[dayKey] = newDay

	// Validate the full merged result (base + overlay) before writing so a
	// broken YAML can't sneak in.
	c2, err := config.Load(cfgPath)
	if err != nil {
		return "", err
	}
	for k, v := range wrap.Days {
		c2.Days[k] = v
	}
	if err := c2.Validate(); err != nil {
		return "", fmt.Errorf("merged config invalid: %w", err)
	}

	if err := writeYAMLAtomic(overlayPath, wrap); err != nil {
		return "", err
	}
	return prettySummaryForOneDay(newDay, dayKey), nil
}

// saveOneTimeForDay commits a single DayConfig to one_time_overrides.yaml for
// the next occurrence of `dayKey` in the config's timezone. Returns the
// pretty summary line for the chat reply, and the ISO date ("2026-04-26") it
// attached the override to.
func saveOneTimeForDay(cfgPath, dayKey string, s *setupSession) (string, string, error) {
	dayKey = strings.ToLower(strings.TrimSpace(dayKey))
	if _, ok := dayKeyToWeekday[dayKey]; !ok {
		return "", "", fmt.Errorf("unknown weekday %q", dayKey)
	}
	if s == nil || len(s.Candidates[dayKey]) == 0 {
		return "", "", fmt.Errorf("no candidates for %s in this setup session", dayKey)
	}

	newDay, err := buildDayConfigForKey(s, dayKey)
	if err != nil {
		return "", "", err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "", "", err
	}
	if err := cfg.Validate(); err != nil {
		return "", "", err
	}
	loc := cfg.Location()
	targetDate := nextDateForDayKey(dayKey, loc)
	if targetDate == "" {
		return "", "", fmt.Errorf("could not compute next %s date", dayKey)
	}

	// Read existing overrides file, merge our new entry, drop expired
	// entries, write back.
	overridesPath := oneTimeOverridesPath()
	type overridesFile struct {
		Version   int                         `yaml:"version"`
		Overrides map[string]config.DayConfig `yaml:"overrides"`
	}
	var wrap overridesFile
	if existing, err := os.ReadFile(overridesPath); err == nil {
		_ = yaml.Unmarshal(existing, &wrap)
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("read overrides: %w", err)
	}
	if wrap.Overrides == nil {
		wrap.Overrides = map[string]config.DayConfig{}
	}
	wrap.Version = 1
	today := time.Now().In(loc).Format("2006-01-02")
	for k := range wrap.Overrides {
		if k < today {
			delete(wrap.Overrides, k)
		}
	}
	wrap.Overrides[targetDate] = newDay

	if err := writeYAMLAtomic(overridesPath, wrap); err != nil {
		return "", "", err
	}
	return prettySummaryForOneDay(newDay, dayKey), targetDate, nil
}

// buildDayConfigForKey produces the same DayConfig shape buildDayMapFromSession
// would produce for a single weekday key. Extracted so both persistent and
// one-time saves share the picks→DayConfig rules.
func buildDayConfigForKey(s *setupSession, dayKey string) (config.DayConfig, error) {
	cands := s.Candidates[dayKey]
	picks := s.Picks[dayKey]
	if len(picks) == 0 {
		return config.DayConfig{Enabled: false}, nil
	}
	seen := make(map[int]bool)
	var opts []config.ClassOption
	for _, idx := range picks {
		if idx < 0 || idx >= len(cands) {
			return config.DayConfig{}, fmt.Errorf("%s: invalid pick index %d", dayKey, idx)
		}
		if seen[idx] {
			continue
		}
		seen[idx] = true
		c := cands[idx]
		opts = append(opts, config.ClassOption{Time: c.Time, Category: c.Category})
	}
	return config.DayConfig{Enabled: true, Options: opts}, nil
}

// nextDateForDayKey returns the ISO date of the next occurrence of dayKey in
// loc. Today counts as "today" — so if dayKey is Sunday and it's currently
// Sunday afternoon in loc, the returned date is today, NOT next Sunday. The
// user's intent for "Just this Sunday" is the imminent one; the booking
// window check that guards against already-past class_starts lives in
// NextOptions, not here.
func nextDateForDayKey(dayKey string, loc *time.Location) string {
	wd, ok := dayKeyToWeekday[strings.ToLower(dayKey)]
	if !ok {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 14; i++ {
		d := start.AddDate(0, 0, i)
		if d.Weekday() == wd {
			return d.Format("2006-01-02")
		}
	}
	return ""
}

// prettySummaryForOneDay makes a one-line human summary like "08:00 Hall B →
// 08:00 Hall A" or "rest day".
func prettySummaryForOneDay(d config.DayConfig, dayKey string) string {
	if !d.Enabled {
		return fmt.Sprintf("%s: rest day", dayKey)
	}
	var parts []string
	if len(d.Options) > 0 {
		for _, o := range d.Options {
			parts = append(parts, fmt.Sprintf("%s %s", o.Time, truncateRunes(strings.TrimSpace(o.Category), 24)))
		}
	} else if d.Time != "" {
		parts = append(parts, fmt.Sprintf("%s %s", d.Time, truncateRunes(strings.TrimSpace(d.Category), 24)))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s: (no options)", dayKey)
	}
	return fmt.Sprintf("%s: %s", dayKey, strings.Join(parts, " → "))
}

// writeYAMLAtomic marshals v and writes to path via tmp + rename, 0o600. Keeps
// parent dir creation local so callers don't have to repeat it.
func writeYAMLAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

