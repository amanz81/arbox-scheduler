package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/amanz81/arbox-scheduler/internal/config"
)

// mkSetupSession makes a minimal setupSession with one Sunday candidate the
// user has "picked". We deliberately keep the candidate list short (just
// what's needed for the save path) — the full fetch flow lives in
// setup_candidates_test.go.
func mkSetupSession(dayKey string) *setupSession {
	return &setupSession{
		Version: 1,
		Candidates: map[string][]setupCandidate{
			dayKey: {
				{Label: "08:00 · Hall B", Time: "08:00", Category: "Hall B"},
				{Label: "08:00 · Hall A", Time: "08:00", Category: "Hall A"},
			},
		},
		Picks: map[string][]int{dayKey: {1}}, // user picked Hall A (index 1)
	}
}

// tmpCfgYAML writes a minimal config.yaml that cfg.Load + Validate accept,
// in a temp dir that the tests' env vars point to.
func tmpCfgYAML(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	raw := `timezone: Asia/Jerusalem
default_time: "08:00"
days:
  sunday:
    enabled: true
    options:
      - { time: "08:00", category: "Hall B" }
  tuesday:
    enabled: false
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSavePersistentForDay_WritesSingleWeekdayToOverlay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := tmpCfgYAML(t, dir)
	t.Setenv("ARBOX_ENV_FILE", filepath.Join(dir, ".env"))

	s := mkSetupSession("sunday")
	summary, err := savePersistentForDay(cfgPath, "sunday", s)
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if summary == "" {
		t.Errorf("summary should be non-empty")
	}

	// Read back the overlay and confirm only sunday was written.
	overlay := userPlanOverlayPath()
	raw, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatalf("overlay missing: %v", err)
	}
	var wrap struct {
		Days map[string]config.DayConfig `yaml:"days"`
	}
	if err := yaml.Unmarshal(raw, &wrap); err != nil {
		t.Fatalf("overlay unparseable: %v", err)
	}
	if len(wrap.Days) != 1 {
		t.Errorf("expected only 1 day in overlay, got %d", len(wrap.Days))
	}
	sun := wrap.Days["sunday"]
	if !sun.Enabled || len(sun.Options) != 1 || sun.Options[0].Category != "Hall A" {
		t.Errorf("sunday shape wrong: %+v", sun)
	}
}

func TestSavePersistentForDay_PreservesOtherWeekdays(t *testing.T) {
	dir := t.TempDir()
	cfgPath := tmpCfgYAML(t, dir)
	t.Setenv("ARBOX_ENV_FILE", filepath.Join(dir, ".env"))

	// Pre-populate overlay with an existing Tuesday entry.
	overlay := userPlanOverlayPath()
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	pre := `days:
  tuesday:
    enabled: true
    options:
      - { time: "09:00", category: "Weightlifting" }
`
	if err := os.WriteFile(overlay, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	s := mkSetupSession("sunday")
	if _, err := savePersistentForDay(cfgPath, "sunday", s); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	raw, _ := os.ReadFile(overlay)
	var wrap struct {
		Days map[string]config.DayConfig `yaml:"days"`
	}
	_ = yaml.Unmarshal(raw, &wrap)
	if _, ok := wrap.Days["tuesday"]; !ok {
		t.Errorf("tuesday entry lost — save should be per-weekday, not a clobber")
	}
	if _, ok := wrap.Days["sunday"]; !ok {
		t.Errorf("sunday entry not written")
	}
}

func TestSaveOneTimeForDay_WritesOverrideFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := tmpCfgYAML(t, dir)
	t.Setenv("ARBOX_ENV_FILE", filepath.Join(dir, ".env"))
	t.Setenv("ARBOX_OVERRIDES_FILE", filepath.Join(dir, "overrides.yaml"))

	s := mkSetupSession("sunday")
	summary, isoDate, err := saveOneTimeForDay(cfgPath, "sunday", s)
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if summary == "" {
		t.Errorf("summary empty")
	}
	if _, err := time.Parse("2006-01-02", isoDate); err != nil {
		t.Errorf("isoDate not a valid date: %q", isoDate)
	}

	raw, err := os.ReadFile(oneTimeOverridesPath())
	if err != nil {
		t.Fatalf("overrides file missing: %v", err)
	}
	type overridesFile struct {
		Version   int                         `yaml:"version"`
		Overrides map[string]config.DayConfig `yaml:"overrides"`
	}
	var wrap overridesFile
	if err := yaml.Unmarshal(raw, &wrap); err != nil {
		t.Fatalf("overrides unparseable: %v", err)
	}
	if wrap.Version != 1 {
		t.Errorf("expected version 1, got %d", wrap.Version)
	}
	entry, ok := wrap.Overrides[isoDate]
	if !ok {
		t.Fatalf("override for %s missing from file", isoDate)
	}
	if !entry.Enabled || entry.Options[0].Category != "Hall A" {
		t.Errorf("override shape wrong: %+v", entry)
	}
}

func TestSaveOneTimeForDay_EmptyPicksBecomesRestDay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := tmpCfgYAML(t, dir)
	t.Setenv("ARBOX_ENV_FILE", filepath.Join(dir, ".env"))
	t.Setenv("ARBOX_OVERRIDES_FILE", filepath.Join(dir, "overrides.yaml"))

	// Picks empty → "skip this {weekday}" semantic.
	s := mkSetupSession("sunday")
	s.Picks["sunday"] = nil

	_, _, err := saveOneTimeForDay(cfgPath, "sunday", s)
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}
	raw, _ := os.ReadFile(oneTimeOverridesPath())
	var wrap struct {
		Overrides map[string]config.DayConfig `yaml:"overrides"`
	}
	_ = yaml.Unmarshal(raw, &wrap)
	for _, v := range wrap.Overrides {
		if v.Enabled {
			t.Errorf("empty picks should yield disabled override, got %+v", v)
		}
	}
}

func TestSaveOneTimeForDay_ExpiredEntriesPruned(t *testing.T) {
	dir := t.TempDir()
	cfgPath := tmpCfgYAML(t, dir)
	t.Setenv("ARBOX_ENV_FILE", filepath.Join(dir, ".env"))
	overridesPath := filepath.Join(dir, "overrides.yaml")
	t.Setenv("ARBOX_OVERRIDES_FILE", overridesPath)

	// Pre-seed with an expired entry (30 days ago) that should be dropped.
	past := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	pre := "version: 1\noverrides:\n  \"" + past + "\":\n    enabled: false\n"
	if err := os.WriteFile(overridesPath, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	s := mkSetupSession("sunday")
	if _, _, err := saveOneTimeForDay(cfgPath, "sunday", s); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	raw, _ := os.ReadFile(overridesPath)
	var wrap struct {
		Overrides map[string]config.DayConfig `yaml:"overrides"`
	}
	_ = yaml.Unmarshal(raw, &wrap)
	if _, stillHere := wrap.Overrides[past]; stillHere {
		t.Errorf("expired override %s should have been pruned during save", past)
	}
}
