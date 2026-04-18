package notify

import (
	"testing"
	"time"
)

func TestEscapeMarkdownV2(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"a.b", `a\.b`},
		{"1-2", `1\-2`},
		{`a*b`, `a\*b`},
		{`(x)`, `\(x\)`},
		{`[x]`, `\[x\]`},
		{`a\b`, `a\\b`},
		{`\`, `\\`},
		{"hello world", "hello world"},
	}
	for _, tc := range cases {
		got := EscapeMarkdownV2(tc.in)
		if got != tc.want {
			t.Errorf("EscapeMarkdownV2(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatMessage_MarkdownV2_escapesBody(t *testing.T) {
	m := Message{
		Event: EventError,
		Text:  "bad: 1.2 (see log)",
	}
	got := formatMessage(m)
	// Header + escaped body: dots and parens backslash-prefixed
	wantPrefix := "⚠️ *Daemon error*\n"
	if len(got) <= len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("unexpected prefix: %q", got)
	}
	if !containsAll(got, `bad: 1\.2 \(see log\)`) {
		t.Fatalf("body not escaped: %q", got)
	}
}

func TestFormatMessage_classTimeInCode(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatal(err)
	}
	m := Message{
		Event:      EventBooked,
		Text:       "done",
		ClassStart: time.Date(2026, 4, 19, 8, 0, 0, 0, loc),
	}
	got := formatMessage(m)
	// Monospace class line; zone abbreviation after time varies by DST.
	if !containsAll(got, "class: `") || !containsAll(got, "2026-04-19") || !containsAll(got, "08:00") {
		t.Fatalf("expected monospace class line in output: %q", got)
	}
}

func containsAll(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
