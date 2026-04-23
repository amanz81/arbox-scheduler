package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// baseCfg builds a minimal validated Config for Asia/Jerusalem with a
// Sunday 08:00 Hall B recurring plan — the real production shape in
// miniature, so override precedence tests exercise both the date match
// and the weekday-fallback path.
func baseCfg(t *testing.T) *Config {
	t.Helper()
	c := &Config{
		Timezone: "Asia/Jerusalem",
		Days: map[string]DayConfig{
			"sunday": {
				Enabled: true,
				Options: []ClassOption{{Time: "08:00", Category: "Hall B"}},
			},
			"monday": {Enabled: false},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate base cfg: %v", err)
	}
	return c
}

func TestOptionsForDate_NoOverrides_FallsBackToWeekday(t *testing.T) {
	c := baseCfg(t)
	loc := c.Location()
	sunday := time.Date(2026, 4, 26, 10, 0, 0, 0, loc) // a real Sunday
	opts := c.OptionsForDate(sunday)
	if len(opts) != 1 || opts[0].Category != "Hall B" {
		t.Fatalf("want [Hall B], got %+v", opts)
	}
}

func TestOptionsForDate_OverrideReplacesWeekday(t *testing.T) {
	c := baseCfg(t)
	loc := c.Location()
	c.OneTimeOverrides = OneTimeOverrides{
		"2026-04-26": {
			Enabled: true,
			Options: []ClassOption{{Time: "09:00", Category: "Hall A"}},
		},
	}
	sunday := time.Date(2026, 4, 26, 10, 0, 0, 0, loc)
	opts := c.OptionsForDate(sunday)
	if len(opts) != 1 {
		t.Fatalf("want 1 option, got %d: %+v", len(opts), opts)
	}
	if opts[0].Time != "09:00" || opts[0].Category != "Hall A" {
		t.Errorf("override not applied: %+v", opts[0])
	}
	// The following Sunday has no override → falls back to weekday plan.
	nextSunday := sunday.AddDate(0, 0, 7)
	opts2 := c.OptionsForDate(nextSunday)
	if len(opts2) != 1 || opts2[0].Category != "Hall B" {
		t.Errorf("next Sunday should fall back to Hall B, got %+v", opts2)
	}
}

func TestOptionsForDate_OverrideDisabledYieldsRestDay(t *testing.T) {
	c := baseCfg(t)
	loc := c.Location()
	c.OneTimeOverrides = OneTimeOverrides{
		"2026-04-26": {Enabled: false}, // "skip just this Sunday"
	}
	sunday := time.Date(2026, 4, 26, 10, 0, 0, 0, loc)
	if opts := c.OptionsForDate(sunday); len(opts) != 0 {
		t.Errorf("disabled override should yield no options, got %+v", opts)
	}
}

func TestMergeOverridesFromFile_HappyPath(t *testing.T) {
	c := baseCfg(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "one_time_overrides.yaml")

	// Pick a date far enough in the future that it's not pruned as "expired"
	// regardless of when the test runs.
	future := time.Now().In(c.Location()).AddDate(0, 0, 7).Format("2006-01-02")
	raw := "version: 1\noverrides:\n  \"" + future + "\":\n    enabled: true\n    options:\n      - { time: \"09:00\", category: \"Hall A\" }\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.MergeOverridesFromFile(path); err != nil {
		t.Fatal(err)
	}
	override, ok := c.OneTimeOverrides[future]
	if !ok {
		t.Fatalf("override for %s not loaded", future)
	}
	if !override.Enabled || len(override.Options) != 1 {
		t.Errorf("override shape wrong: %+v", override)
	}
}

func TestMergeOverridesFromFile_ExpiredAreDropped(t *testing.T) {
	c := baseCfg(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "one_time_overrides.yaml")
	past := time.Now().In(c.Location()).AddDate(0, 0, -10).Format("2006-01-02")
	raw := "version: 1\noverrides:\n  \"" + past + "\":\n    enabled: false\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.MergeOverridesFromFile(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.OneTimeOverrides[past]; ok {
		t.Errorf("expired override %s should have been skipped", past)
	}
}

func TestMergeOverridesFromFile_InvalidDateKey(t *testing.T) {
	c := baseCfg(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "one_time_overrides.yaml")
	raw := "version: 1\noverrides:\n  \"2026-04-26 \":\n    enabled: false\n" // trailing space
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	// Whitespace tolerated by TrimSpace — valid.
	if err := c.MergeOverridesFromFile(path); err != nil {
		t.Fatalf("whitespace-padded date key should parse: %v", err)
	}
	// But "not-a-date" is rejected outright.
	raw2 := "version: 1\noverrides:\n  \"not-a-date\":\n    enabled: false\n"
	if err := os.WriteFile(path, []byte(raw2), 0o600); err != nil {
		t.Fatal(err)
	}
	c.OneTimeOverrides = nil // reset
	if err := c.MergeOverridesFromFile(path); err == nil {
		t.Errorf("non-date key should error")
	}
}

func TestMergeOverridesFromFile_MissingIsNoOp(t *testing.T) {
	c := baseCfg(t)
	err := c.MergeOverridesFromFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Errorf("missing file should be a no-op, got %v", err)
	}
	if len(c.OneTimeOverrides) != 0 {
		t.Errorf("no overrides should be loaded")
	}
}
