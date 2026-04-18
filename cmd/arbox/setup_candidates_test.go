package main

import "testing"

func TestShortenCategoryForButton(t *testing.T) {
	cases := map[string]string{
		"CrossFit- Hall A":     "Hall A",
		"CrossFit-Hall A":      "Hall A",
		"Crossfit Hall B":      "Hall B",
		"Weightlifting Hall B": "Weightlifting Hall B",
		"Open Workout- Hall B": "Open Workout- Hall B",
		" CrossFit- Hall A ":   "Hall A",
		"":                     "",
	}
	for in, want := range cases {
		if got := shortenCategoryForButton(in); got != want {
			t.Errorf("shortenCategoryForButton(%q) = %q, want %q", in, got, want)
		}
	}
}
