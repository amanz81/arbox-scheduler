package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pinHeartbeatStatePath redirects heartbeat.json to the test temp dir for
// the duration of the test, so we never touch the real production file.
func pinHeartbeatStatePath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ARBOX_HEARTBEAT_STATE", filepath.Join(dir, "heartbeat.json"))
}

func TestHeartbeatState_DefaultIsEnabled(t *testing.T) {
	pinHeartbeatStatePath(t)
	hb, err := readHeartbeatState()
	if err != nil {
		t.Fatal(err)
	}
	if !hb.IsEnabled() {
		t.Errorf("default state must be enabled (matches behavior before /heartbeat off existed); got disabled")
	}
	if hb.EnabledExplicitlySet {
		t.Errorf("EnabledExplicitlySet must stay false on a fresh install")
	}
}

func TestHeartbeatState_ExplicitOffPersists(t *testing.T) {
	pinHeartbeatStatePath(t)
	if err := setHeartbeatEnabled(false); err != nil {
		t.Fatal(err)
	}
	hb, _ := readHeartbeatState()
	if hb.IsEnabled() {
		t.Errorf("after /heartbeat off, state must be disabled across reads")
	}
	if !hb.EnabledExplicitlySet {
		t.Errorf("EnabledExplicitlySet should flip to true after a save")
	}
	if hb.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt must be stamped on save")
	}
	// Re-enable, confirm flip.
	if err := setHeartbeatEnabled(true); err != nil {
		t.Fatal(err)
	}
	hb, _ = readHeartbeatState()
	if !hb.IsEnabled() {
		t.Errorf("after /heartbeat on, state must be enabled again")
	}
}

func TestHandleHeartbeatCommand_OnOffStatus(t *testing.T) {
	pinHeartbeatStatePath(t)

	// Show with no args, default state.
	got := handleHeartbeatCommand(nil)
	if !strings.Contains(strings.ToLower(got), "enabled") {
		t.Errorf("status of fresh install should report enabled, got: %s", got)
	}
	if !strings.Contains(got, "Default") {
		t.Errorf("status should mention 'Default; never explicitly changed': %s", got)
	}

	// /heartbeat off → disabled, persists.
	got = handleHeartbeatCommand([]string{"off"})
	if !strings.Contains(strings.ToLower(got), "disabled") {
		t.Errorf("`/heartbeat off` reply should confirm disabled: %s", got)
	}
	hb, _ := readHeartbeatState()
	if hb.IsEnabled() {
		t.Errorf("/heartbeat off must persist as Enabled=false")
	}

	// status now says disabled.
	got = handleHeartbeatCommand(nil)
	if !strings.Contains(strings.ToLower(got), "disabled") {
		t.Errorf("after off, status should say disabled: %s", got)
	}

	// /heartbeat on → enabled.
	got = handleHeartbeatCommand([]string{"on"})
	if !strings.Contains(strings.ToLower(got), "enabled") {
		t.Errorf("`/heartbeat on` reply should confirm enabled: %s", got)
	}
}

func TestHandleHeartbeatCommand_AcceptsAliases(t *testing.T) {
	pinHeartbeatStatePath(t)
	for _, alias := range []string{"enable", "ENABLED", "Enable"} {
		_ = handleHeartbeatCommand([]string{"off"})
		got := handleHeartbeatCommand([]string{alias})
		if !strings.Contains(strings.ToLower(got), "enabled") {
			t.Errorf("alias %q should enable, got: %s", alias, got)
		}
	}
	for _, alias := range []string{"disable", "MUTE", "Off"} {
		_ = handleHeartbeatCommand([]string{"on"})
		got := handleHeartbeatCommand([]string{alias})
		if !strings.Contains(strings.ToLower(got), "disabled") {
			t.Errorf("alias %q should disable, got: %s", alias, got)
		}
	}
}

func TestHandleHeartbeatCommand_RejectsUnknownArg(t *testing.T) {
	pinHeartbeatStatePath(t)
	got := handleHeartbeatCommand([]string{"maybe"})
	if !strings.Contains(strings.ToLower(got), "usage") {
		t.Errorf("unknown arg should print usage, got: %s", got)
	}
	hb, _ := readHeartbeatState()
	if hb.EnabledExplicitlySet {
		t.Errorf("unknown arg must NOT mutate state")
	}
}

// TestHeartbeat_DisabledMutesEvenOnThursday verifies the daemon path: when
// the user has /heartbeat off, maybeDailyHeartbeat sends nothing even on a
// Thursday. Skipped on non-Thursdays since maybeDailyHeartbeat reads
// time.Now() directly (same caveat as the other heartbeat tests).
func TestHeartbeat_DisabledMutesEvenOnThursday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	if time.Now().In(loc).Weekday() != time.Thursday {
		t.Skip("disabled-mutes test runs only on Thursday")
	}
	pinHeartbeatStatePath(t)
	if err := setHeartbeatEnabled(false); err != nil {
		t.Fatal(err)
	}
	notif := &captureNotifier{}
	lastDay := "1970-01-01"
	maybeDailyHeartbeat(notif, loc, &lastDay, "alive · would have sent this")
	if len(notif.events) != 0 {
		t.Errorf("disabled state must suppress the Thursday send; got %d notifications", len(notif.events))
	}
	// And lastDay is still bumped, so a later /heartbeat on doesn't burst-send.
	if lastDay == "1970-01-01" {
		t.Errorf("lastDay must be bumped even when suppressed, otherwise /heartbeat on causes a backlog burst")
	}
}
