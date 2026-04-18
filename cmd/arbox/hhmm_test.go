package main

import "testing"

func TestHHMM_normalizes(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"08:00":     "08:00",
		"08:00:00":  "08:00",
		"8:00":      "08:00",
		"8:00:00":   "08:00",
		"  09:30 ":  "09:30",
		"21:05:30":  "21:05",
		"7:5":       "07:5",
	}
	for in, want := range cases {
		if got := hhmm(in); got != want {
			t.Errorf("hhmm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHHMMMinutes(t *testing.T) {
	if m, ok := parseHHMMMinutes("09:30"); !ok || m != 570 {
		t.Errorf("09:30 -> %d %v", m, ok)
	}
	if _, ok := parseHHMMMinutes("xx"); ok {
		t.Errorf("xx should not parse")
	}
}
