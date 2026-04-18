package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pauseState represents a global "do not auto-book" window persisted to disk.
// Empty file or PausedUntil in the past => not paused.
type pauseState struct {
	PausedUntil time.Time `json:"paused_until"`
	Reason      string    `json:"reason,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func readPauseState() (pauseState, error) {
	var s pauseState
	b, err := os.ReadFile(pauseStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("decode pause state: %w", err)
	}
	return s, nil
}

func writePauseState(s pauseState) error {
	path := pauseStatePath()
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

func clearPauseState() error {
	err := os.Remove(pauseStatePath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsActive reports whether `now` is still inside the paused window.
func (s pauseState) IsActive(now time.Time) bool {
	return !s.PausedUntil.IsZero() && now.Before(s.PausedUntil)
}

// Summary returns a short one-line tag suitable for /status / /morning headers.
// loc is used to render the until-time in the user's timezone.
func (s pauseState) Summary(now time.Time, loc *time.Location) string {
	if !s.IsActive(now) {
		return ""
	}
	t := s.PausedUntil
	if loc != nil {
		t = t.In(loc)
	}
	left := s.PausedUntil.Sub(now).Round(time.Minute)
	tag := fmt.Sprintf("PAUSED until %s (~%s left)", t.Format("Mon 02 Jan 15:04 MST"), shortDuration(left))
	if r := strings.TrimSpace(s.Reason); r != "" {
		tag += " — " + r
	}
	return tag
}

func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	if hours < 24 {
		if mins == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	days := hours / 24
	hours = hours % 24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, hours)
}

// parsePauseArgs accepts:
//   (no args)                    -> 24h
//   "Nh" / "Nd" / "Nm"           -> duration (e.g. "3d", "12h", "90m")
//   "until YYYY-MM-DD"           -> end-of-day local TZ on that date
//   "until YYYY-MM-DD HH:MM"     -> exact time in local TZ
// Trailing free-text (e.g. "vacation") becomes the reason.
func parsePauseArgs(args []string, now time.Time, loc *time.Location) (until time.Time, reason string, err error) {
	if loc == nil {
		loc = time.UTC
	}
	if len(args) == 0 {
		return now.Add(24 * time.Hour).In(loc), "", nil
	}
	if strings.EqualFold(args[0], "until") {
		if len(args) < 2 {
			return time.Time{}, "", fmt.Errorf("usage: /pause until YYYY-MM-DD [HH:MM] [reason...]")
		}
		datePart := args[1]
		consumed := 2
		var t time.Time
		if len(args) >= 3 && looksLikeHHMM(args[2]) {
			t, err = time.ParseInLocation("2006-01-02 15:04", datePart+" "+args[2], loc)
			consumed = 3
		} else {
			t, err = time.ParseInLocation("2006-01-02", datePart, loc)
			if err == nil {
				// end of that day, local tz
				t = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 0, 0, loc)
			}
		}
		if err != nil {
			return time.Time{}, "", fmt.Errorf("bad date/time after 'until': %w", err)
		}
		if !t.After(now) {
			return time.Time{}, "", fmt.Errorf("'until' time is in the past")
		}
		return t, strings.TrimSpace(strings.Join(args[consumed:], " ")), nil
	}
	dur, err := parseShortDuration(args[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("bad duration %q (try '24h', '3d', '90m', or 'until YYYY-MM-DD')", args[0])
	}
	if dur <= 0 || dur > 90*24*time.Hour {
		return time.Time{}, "", fmt.Errorf("duration out of range (1m..90d)")
	}
	return now.Add(dur).In(loc), strings.TrimSpace(strings.Join(args[1:], " ")), nil
}

func looksLikeHHMM(s string) bool {
	if len(s) < 4 || strings.IndexByte(s, ':') < 0 {
		return false
	}
	_, err := time.Parse("15:04", s)
	return err == nil
}

// parseShortDuration accepts Nm / Nh / Nd plus Go's standard durations.
func parseShortDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
