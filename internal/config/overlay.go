package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// MergeDaysFromFile merges the top-level `days` map from a YAML file into c.
// The overlay is typically generated on the server (e.g. Telegram /setup)
// and stored on a persistent volume. Only the `days` key is read; all other
// keys in the overlay file are ignored.
//
// If the file does not exist, MergeDaysFromFile is a no-op.
func (c *Config) MergeDaysFromFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read overlay %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}

	var wrap struct {
		Days map[string]DayConfig `yaml:"days"`
	}
	if err := yaml.Unmarshal(b, &wrap); err != nil {
		return fmt.Errorf("parse overlay %s: %w", path, err)
	}
	if len(wrap.Days) == 0 {
		return nil
	}
	if c.Days == nil {
		c.Days = map[string]DayConfig{}
	}
	for k, v := range wrap.Days {
		c.Days[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return nil
}
