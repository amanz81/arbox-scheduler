package main

import (
	"os"
	"path/filepath"
)

// dataDir is the directory that holds persistent runtime files (.env,
// user_plan.yaml, setup_session.json). On Fly this is /data; locally it is
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
