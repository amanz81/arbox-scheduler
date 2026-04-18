package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndValidate_OK(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Asia/Jerusalem
default_time: "09:00"
days:
  sunday:   { enabled: true, time: "08:00" }
  monday:   { enabled: true, time: "08:30" }
  tuesday:  { enabled: true }
  wednesday: { enabled: false }
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
	if opts := c.OptionsFor(time.Sunday); len(opts) != 1 || opts[0].Time != "08:00" {
		t.Errorf("sunday shorthand: %+v", opts)
	}
	if opts := c.OptionsFor(time.Tuesday); len(opts) != 1 || opts[0].Time != "09:00" {
		t.Errorf("tuesday should fall back to default_time: %+v", opts)
	}
	if opts := c.OptionsFor(time.Wednesday); opts != nil {
		t.Errorf("wednesday should be disabled: %+v", opts)
	}
}

func TestLoadAndValidate_OptionsList(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Asia/Jerusalem
days:
  monday:
    enabled: true
    options:
      - { time: "08:30", category: "Crossfit Hall B" }
      - { time: "08:30", category: "CrossFit- Hall A" }
  tuesday:
    enabled: true
    options:
      - { time: "09:00" }
      - { time: "08:00" }
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	mon := c.OptionsFor(time.Monday)
	if len(mon) != 2 {
		t.Fatalf("monday options: %+v", mon)
	}
	if mon[0].Category != "Crossfit Hall B" || mon[1].Category != "CrossFit- Hall A" {
		t.Errorf("monday priority: %+v", mon)
	}
	tue := c.OptionsFor(time.Tuesday)
	if len(tue) != 2 || tue[0].Time != "09:00" || tue[1].Time != "08:00" {
		t.Errorf("tuesday options: %+v", tue)
	}
}

func TestValidate_TimeAndOptionsMutuallyExclusive(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Asia/Jerusalem
days:
  monday:
    enabled: true
    time: "08:30"
    options: [{time: "09:00"}]
`)
	c, _ := Load(path)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "either `time:` OR `options:`") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestValidate_UnknownDay(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Asia/Jerusalem
default_time: "09:00"
days:
  funday: { enabled: true, time: "09:00" }
`)
	c, _ := Load(path)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown day key") {
		t.Fatalf("expected unknown day error, got %v", err)
	}
}

func TestValidate_BadTimeInOptions(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Asia/Jerusalem
days:
  monday:
    enabled: true
    options:
      - { time: "8:30" }
`)
	c, _ := Load(path)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "not HH:MM") {
		t.Fatalf("expected HH:MM error, got %v", err)
	}
}

func TestValidate_EnabledDayWithoutTimeOrDefault(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Asia/Jerusalem
days:
  monday: { enabled: true }
`)
	c, _ := Load(path)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "no time/options and no default_time") {
		t.Fatalf("expected missing-time error, got %v", err)
	}
}

func TestValidate_BadTimezone(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Atlantis/Lost
default_time: "09:00"
days:
  monday: { enabled: true, time: "08:30" }
`)
	c, _ := Load(path)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid timezone") {
		t.Fatalf("expected invalid timezone error, got %v", err)
	}
}

func TestCategoryFilter_Parses(t *testing.T) {
	path := writeTempConfig(t, `
timezone: Asia/Jerusalem
default_time: "09:00"
category_filter:
  include: ["Hall A", "Hall B"]
  exclude: ["Open Workout", "Weightlifting"]
days:
  monday: { enabled: true, time: "08:30" }
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.CategoryFilter.Include) != 2 || c.CategoryFilter.Include[0] != "Hall A" {
		t.Errorf("include: %+v", c.CategoryFilter.Include)
	}
	if len(c.CategoryFilter.Exclude) != 2 || c.CategoryFilter.Exclude[1] != "Weightlifting" {
		t.Errorf("exclude: %+v", c.CategoryFilter.Exclude)
	}
}
