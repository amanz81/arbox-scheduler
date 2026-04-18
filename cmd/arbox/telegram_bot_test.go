package main

import (
	"strings"
	"testing"
)

func TestSplitTelegramByteChunks_single(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := splitTelegramByteChunks(s, 200)
	if len(got) != 1 || got[0] != s {
		t.Fatalf("want 1 chunk, got %d first len %d", len(got), len(got[0]))
	}
}

func TestSplitTelegramByteChunks_multilineUTF8(t *testing.T) {
	line := "ééé\n" // 7 bytes per line in UTF-8
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString(line)
	}
	s := b.String()
	got := splitTelegramByteChunks(s, 64)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}
	rejoined := strings.Join(got, "")
	if rejoined != s {
		t.Fatalf("rejoin mismatch: lens %d vs %d", len(rejoined), len(s))
	}
	for _, ch := range got {
		if len(ch) > 64 {
			t.Fatalf("chunk len %d > max", len(ch))
		}
	}
}

func TestJoinByteChunks(t *testing.T) {
	if got := joinByteChunks([]string{"a", "b"}); got != "ab" {
		t.Fatalf("got %q", got)
	}
	if got := joinByteChunks(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}
