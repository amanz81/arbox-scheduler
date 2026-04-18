package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeDaysFromFile(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "overlay.yaml")
	raw := []byte(`days:
  monday:
    enabled: true
    options:
      - { time: "10:00", category: "TestCat" }
`)
	if err := os.WriteFile(overlay, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Config{
		Timezone: "Asia/Jerusalem",
		Days: map[string]DayConfig{
			"monday": {Enabled: true, Options: []ClassOption{{Time: "08:30", Category: "Old"}}},
		},
	}
	if err := c.MergeDaysFromFile(overlay); err != nil {
		t.Fatal(err)
	}
	m := c.Days["monday"]
	if len(m.Options) != 1 || m.Options[0].Time != "10:00" {
		t.Fatalf("merge failed: %+v", m)
	}
}

func TestMergeDaysFromFile_missingNoOp(t *testing.T) {
	c := &Config{Timezone: "UTC", Days: map[string]DayConfig{}}
	if err := c.MergeDaysFromFile(filepath.Join(t.TempDir(), "nope.yaml")); err != nil {
		t.Fatal(err)
	}
}
