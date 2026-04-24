package main

import (
	"os"
	"path/filepath"
)

// dataDir is the directory that holds persistent runtime files (.env,
// user_plan.yaml, setup_session.json). On the production Oracle VM this is
// ~/arbox/data/; locally it is
// the directory containing .env (often the repo root).
func dataDir() string {
	p := os.Getenv("ARBOX_ENV_FILE")
	if p == "" {
		p = ".env"
	}
	d := filepath.Dir(filepath.Clean(p))
	if d == "." || d == "" {
		return "."
	}
	return d
}

func userPlanOverlayPath() string {
	if v := os.Getenv("ARBOX_USER_PLAN"); v != "" {
		return v
	}
	return filepath.Join(dataDir(), "user_plan.yaml")
}

func setupSessionPath() string {
	if v := os.Getenv("ARBOX_SETUP_SESSION"); v != "" {
		return v
	}
	return filepath.Join(dataDir(), "setup_session.json")
}

func pauseStatePath() string {
	if v := os.Getenv("ARBOX_PAUSE_STATE"); v != "" {
		return v
	}
	return filepath.Join(dataDir(), "pause.json")
}

func bookingAttemptsPath() string {
	if v := os.Getenv("ARBOX_BOOKING_ATTEMPTS"); v != "" {
		return v
	}
	return filepath.Join(dataDir(), "booking_attempts.json")
}

// oneTimeOverridesPath is the YAML file that maps YYYY-MM-DD → DayConfig
// overrides. Set via ARBOX_OVERRIDES_FILE for tests; default lives next to
// user_plan.yaml so both follow the same ARBOX_ENV_FILE → dataDir() chain.
func oneTimeOverridesPath() string {
	if v := os.Getenv("ARBOX_OVERRIDES_FILE"); v != "" {
		return v
	}
	return filepath.Join(dataDir(), "one_time_overrides.yaml")
}

// heartbeatStatePath is the JSON file that records whether the Thursday
// heartbeat is enabled. Set via ARBOX_HEARTBEAT_STATE for tests.
func heartbeatStatePath() string {
	if v := os.Getenv("ARBOX_HEARTBEAT_STATE"); v != "" {
		return v
	}
	return filepath.Join(dataDir(), "heartbeat.json")
}
