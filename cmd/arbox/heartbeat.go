package main

// Heartbeat enable/disable state.
//
// The Thursday-only one-line heartbeat (see maybeDailyHeartbeat in daemon.go)
// can be turned off entirely from Telegram via /heartbeat off. Useful when
// the user wants zero scheduled pings (silent mode) but still wants
// per-event booking notifications + on-demand /status / /selftest. Default:
// enabled (matches behavior before this file existed).
//
// State lives in heartbeat.json next to the other runtime state files
// (pause.json, booking_attempts.json, etc.) so it survives daemon restarts.
// Mode 0o600. Atomic write via tmp + rename.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// heartbeatState is the on-disk shape. An empty struct (Enabled=false,
// EnabledExplicitlySet=false) is the special "no preference saved yet"
// state and means "use the default" (enabled).
type heartbeatState struct {
	Enabled              bool      `json:"enabled"`
	UpdatedAt            time.Time `json:"updated_at,omitempty"`
	EnabledExplicitlySet bool      `json:"enabled_explicitly_set,omitempty"`
}

// IsEnabled returns true unless the user has explicitly disabled heartbeats.
// A missing file or a never-saved state defaults to true.
func (s heartbeatState) IsEnabled() bool {
	if !s.EnabledExplicitlySet {
		return true
	}
	return s.Enabled
}

func readHeartbeatState() (heartbeatState, error) {
	var s heartbeatState
	b, err := os.ReadFile(heartbeatStatePath())
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
		return s, fmt.Errorf("decode heartbeat state: %w", err)
	}
	return s, nil
}

// writeHeartbeatState persists the user's preference. The
// EnabledExplicitlySet flag is set by setHeartbeatEnabled so a future
// "I never touched this" reset is distinguishable from "user picked
// enabled=false".
func writeHeartbeatState(s heartbeatState) error {
	path := heartbeatStatePath()
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

// setHeartbeatEnabled is the canonical way to flip the preference from a
// command handler. Stamps UpdatedAt + the EnabledExplicitlySet flag so the
// "no preference saved yet" default-on stops applying.
func setHeartbeatEnabled(enabled bool) error {
	return writeHeartbeatState(heartbeatState{
		Enabled:              enabled,
		EnabledExplicitlySet: true,
		UpdatedAt:            time.Now().UTC(),
	})
}

// handleHeartbeatCommand is the message-body builder for the /heartbeat
// Telegram command:
//
//	/heartbeat        → show current state (enabled/disabled + when changed)
//	/heartbeat on     → enable + persist
//	/heartbeat off    → disable + persist
//	/heartbeat status → alias of /heartbeat with no args
//
// Returns the human-readable reply (caller wraps in "*Heartbeat*\n…" +
// MarkdownV2 escape). Errors are surfaced inline rather than thrown so the
// user gets a useful chat message either way.
func handleHeartbeatCommand(args []string) string {
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "on", "enable", "enabled":
			if err := setHeartbeatEnabled(true); err != nil {
				return "Failed to save preference: " + err.Error()
			}
			return "Enabled. The Thursday one-line heartbeat will resume."
		case "off", "disable", "disabled", "mute":
			if err := setHeartbeatEnabled(false); err != nil {
				return "Failed to save preference: " + err.Error()
			}
			return "Disabled. No more heartbeat pings until you /heartbeat on again. " +
				"Booking notifications and on-demand /status / /selftest still work."
		case "status", "show":
			// fall through to the show-state path below
		default:
			return "Usage:\n" +
				"  /heartbeat            — show current state\n" +
				"  /heartbeat on         — enable Thursday heartbeat\n" +
				"  /heartbeat off        — disable heartbeat (silent)"
		}
	}
	hb, err := readHeartbeatState()
	if err != nil {
		return "Failed to read state: " + err.Error()
	}
	if hb.IsEnabled() {
		out := "Enabled. Sends one line every Thursday in the gym timezone."
		if hb.EnabledExplicitlySet && !hb.UpdatedAt.IsZero() {
			out += "\n(Last toggled: " + hb.UpdatedAt.Format("2006-01-02 15:04 UTC") + ")"
		} else {
			out += "\n(Default; never explicitly changed.)"
		}
		out += "\n\nDisable with /heartbeat off."
		return out
	}
	out := "Disabled. No heartbeat pings will be sent."
	if !hb.UpdatedAt.IsZero() {
		out += "\n(Disabled at: " + hb.UpdatedAt.Format("2006-01-02 15:04 UTC") + ")"
	}
	out += "\n\nRe-enable with /heartbeat on. Booking notifications and on-demand " +
		"commands (/status, /selftest, /morning, /evening) are unaffected."
	return out
}
