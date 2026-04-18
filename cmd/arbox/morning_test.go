package main

import "testing"

func TestParseMorningArgs_defaults(t *testing.T) {
	s, e, d, err := parseMorningArgs(nil, 6, 12, 7)
	if err != nil || s != 6 || e != 12 || d != 1 {
		t.Fatalf("got %d %d %d err=%v", s, e, d, err)
	}
}

func TestParseMorningArgs_range(t *testing.T) {
	s, e, d, err := parseMorningArgs([]string{"8-10"}, 6, 12, 7)
	if err != nil || s != 8 || e != 10 || d != 1 {
		t.Fatalf("got %d %d %d err=%v", s, e, d, err)
	}
}

func TestParseMorningArgs_days(t *testing.T) {
	s, e, d, err := parseMorningArgs([]string{"3"}, 6, 12, 7)
	if err != nil || s != 6 || e != 12 || d != 3 {
		t.Fatalf("got %d %d %d err=%v", s, e, d, err)
	}
}

func TestParseMorningArgs_both(t *testing.T) {
	s, e, d, err := parseMorningArgs([]string{"8-10", "2"}, 6, 12, 7)
	if err != nil || s != 8 || e != 10 || d != 2 {
		t.Fatalf("got %d %d %d err=%v", s, e, d, err)
	}
}

func TestParseMorningArgs_invalid(t *testing.T) {
	if _, _, _, err := parseMorningArgs([]string{"10-8"}, 6, 12, 7); err == nil {
		t.Fatalf("want error for inverted range")
	}
	if _, _, _, err := parseMorningArgs([]string{"99"}, 6, 12, 7); err == nil {
		t.Fatalf("want error for too-many days")
	}
	if _, _, _, err := parseMorningArgs([]string{"abc"}, 6, 12, 7); err == nil {
		t.Fatalf("want error for non-numeric")
	}
}
