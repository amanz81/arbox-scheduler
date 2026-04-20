package main

import (
	"testing"
	"time"
)

// Targeted tests for the terminal-error behavior added after the
// medicalWavierRestricted retry storm on 2026-04-20.

func TestIsTerminalHTTP_514Yes_OthersNo(t *testing.T) {
	// 514 is Arbox's chosen code for business-rule failures; everything
	// else we treat as transient so network hiccups / expired tokens /
	// rate limits get a fresh try on the next tick.
	cases := []struct {
		status int
		want   bool
	}{
		{514, true},
		{0, false},
		{200, false},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{500, false},
		{502, false},
		{503, false},
		{429, false},
	}
	for _, c := range cases {
		if got := isTerminalHTTP(c.status); got != c.want {
			t.Errorf("isTerminalHTTP(%d) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestBookingAttempt_TerminalRoundTrip(t *testing.T) {
	// Persist + reload must preserve Terminal + TerminalUntil, otherwise
	// a daemon restart (deploy, OOM, oracle maintenance) would forget and
	// re-enter the retry storm.
	dir := t.TempDir()
	t.Setenv("ARBOX_BOOKING_ATTEMPTS", dir+"/booking_attempts.json")

	until := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	in := attemptsState{Attempts: map[int]bookingAttempt{
		53268139: {
			ScheduleID:    53268139,
			Result:        resultFailed,
			Message:       "medicalWavierRestricted",
			HTTPStatus:    514,
			When:          time.Now().UTC().Truncate(time.Second),
			Slot:          "2026-04-21 09:00 Weightlifting Hall B",
			Terminal:      true,
			TerminalUntil: until,
		},
	}}
	if err := writeAttemptsState(in); err != nil {
		t.Fatal(err)
	}
	out := readAttemptsState()
	got, ok := out.Attempts[53268139]
	if !ok {
		t.Fatalf("record not round-tripped: %+v", out)
	}
	if !got.Terminal {
		t.Errorf("Terminal flag lost across round-trip")
	}
	if !got.TerminalUntil.Equal(until) {
		t.Errorf("TerminalUntil lost or mangled: got %v want %v", got.TerminalUntil, until)
	}
	if got.HTTPStatus != 514 {
		t.Errorf("HTTPStatus lost: %d", got.HTTPStatus)
	}
}

func TestTerminalBackoff_IsReasonable(t *testing.T) {
	// Keep the backoff in a sane range so we don't spam (<1h) and don't
	// permanently forget (>24h). 6h is the current choice; this test
	// just prevents someone lowering it to "10 * time.Minute" without
	// thinking.
	if TerminalBackoff < 1*time.Hour {
		t.Errorf("TerminalBackoff too short: %v (would risk spam loop)", TerminalBackoff)
	}
	if TerminalBackoff > 24*time.Hour {
		t.Errorf("TerminalBackoff too long: %v (user's action won't be noticed until next day)", TerminalBackoff)
	}
}
