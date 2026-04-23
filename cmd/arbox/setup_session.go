package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amanz81/arbox-scheduler/internal/config"
	"gopkg.in/yaml.v3"
)

// setupSession is persisted JSON while the user is toggling inline buttons.
type setupSession struct {
	Version    int                            `json:"v"`
	Candidates map[string][]setupCandidate    `json:"candidates"`
	Picks      map[string][]int               `json:"picks"` // weekday key -> indices into Candidates[key], in priority order
}

func readSetupSession() (*setupSession, error) {
	path := setupSessionPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s setupSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Picks == nil {
		s.Picks = map[string][]int{}
	}
	return &s, nil
}

func writeSetupSession(s *setupSession) error {
	path := setupSessionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func deleteSetupSession() error {
	err := os.Remove(setupSessionPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// seedSetupPicksFromConfig maps the current merged config onto setup
// candidates (same weekday, time, category substring) so /setup opens with
// the user's saved plan visibly selected.
func seedSetupPicksFromConfig(cfg *config.Config, cands map[string][]setupCandidate) map[string][]int {
	picks := make(map[string][]int)
	for dayKey, row := range cands {
		wd, ok := dayKeyToWeekday[dayKey]
		if !ok {
			continue
		}
		opts := cfg.OptionsFor(wd)
		if len(opts) == 0 {
			continue
		}
		seenIdx := make(map[int]bool)
		for _, co := range opts {
			for i, c := range row {
				if seenIdx[i] {
					continue
				}
				if c.Time != co.Time {
					continue
				}
				coCat := strings.TrimSpace(co.Category)
				if coCat != "" && !strings.Contains(strings.ToLower(c.Category), strings.ToLower(coCat)) {
					continue
				}
				picks[dayKey] = append(picks[dayKey], i)
				seenIdx[i] = true
				break
			}
		}
	}
	return picks
}

// togglePick is RADIO-style: at most one selection per day. Priority-list
// fallback was removed (it was confusing to end up waitlisted on Hall B
// AND booked into Hall A the same morning). New semantics:
//   - tap a new index → becomes the single pick; any prior pick is replaced
//   - tap the currently-picked index → clears it (day becomes a rest day)
//
// Returns a short human action label for the Telegram callback ack.
func togglePick(s *setupSession, dayKey string, idx int) string {
	if s.Picks == nil {
		s.Picks = map[string][]int{}
	}
	cur := s.Picks[dayKey]
	if len(cur) == 1 && cur[0] == idx {
		s.Picks[dayKey] = nil
		return "cleared (rest day)"
	}
	s.Picks[dayKey] = []int{idx}
	if len(cur) == 0 {
		return "selected"
	}
	return "replaced"
}

// writeUserPlanFromSession builds user_plan.yaml from picks + candidates and
// validates the merged config before writing.
func writeUserPlanFromSession(cfgPath string, s *setupSession) error {
	if s == nil || len(s.Candidates) == 0 {
		return fmt.Errorf("no setup session; run /setup first")
	}
	merged, err := buildDayMapFromSession(s)
	if err != nil {
		return err
	}
	c2, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	for k, v := range merged {
		c2.Days[k] = v
	}
	if err := c2.Validate(); err != nil {
		return fmt.Errorf("merged config invalid: %w", err)
	}

	out := struct {
		Days map[string]config.DayConfig `yaml:"days"`
	}{Days: merged}
	raw, err := yaml.Marshal(&out)
	if err != nil {
		return err
	}
	path := userPlanOverlayPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return deleteSetupSession()
}

// buildDayMapFromSession returns only weekdays that appeared in the setup
// session. For each: if user picked ≥1 slot → enabled + options in tap order;
// if candidates existed but picks empty → explicit rest day (disabled).
func buildDayMapFromSession(s *setupSession) (map[string]config.DayConfig, error) {
	out := make(map[string]config.DayConfig)
	for dayKey, cands := range s.Candidates {
		dayKey = strings.ToLower(dayKey)
		picks := s.Picks[dayKey]
		if len(cands) == 0 {
			continue
		}
		if len(picks) == 0 {
			out[dayKey] = config.DayConfig{Enabled: false}
			continue
		}
		seen := make(map[int]bool)
		var opts []config.ClassOption
		for _, idx := range picks {
			if idx < 0 || idx >= len(cands) {
				return nil, fmt.Errorf("%s: invalid pick index %d", dayKey, idx)
			}
			if seen[idx] {
				continue
			}
			seen[idx] = true
			c := cands[idx]
			opts = append(opts, config.ClassOption{Time: c.Time, Category: c.Category})
		}
		out[dayKey] = config.DayConfig{Enabled: true, Options: opts}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nothing to save")
	}
	return out, nil
}

// summarizePicks returns a short human-readable summary of current picks.
func summarizePicks(s *setupSession) string {
	var lines []string
	for _, dayKey := range setupWeekdayOrder {
		picks, ok := s.Picks[dayKey]
		if !ok || len(picks) == 0 {
			continue
		}
		cands := s.Candidates[dayKey]
		var parts []string
		for _, idx := range picks {
			if idx >= 0 && idx < len(cands) {
				parts = append(parts, fmt.Sprintf("%s %s", cands[idx].Time, truncateRunes(cands[idx].Category, 24)))
			}
		}
		if len(parts) > 0 {
			lines = append(lines, fmt.Sprintf("%s: %s", dayKey, strings.Join(parts, " → ")))
		}
	}
	if len(lines) == 0 {
		return "(no slots selected yet)"
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
