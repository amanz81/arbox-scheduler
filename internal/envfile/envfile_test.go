package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_DoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("FOO=from-file\nBAR=baz\n"), 0o600)

	os.Setenv("FOO", "from-real-env")
	t.Cleanup(func() { os.Unsetenv("FOO"); os.Unsetenv("BAR") })

	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("FOO"); got != "from-real-env" {
		t.Errorf("FOO should be preserved from real env, got %q", got)
	}
	if got := os.Getenv("BAR"); got != "baz" {
		t.Errorf("BAR should be loaded, got %q", got)
	}
}

func TestLoad_MissingFileIsOK(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatal(err)
	}
}

func TestUpsert_ReplaceExistingAndAppendNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	initial := "# header\nFOO=old\n\nOTHER=keep\n"
	os.WriteFile(path, []byte(initial), 0o600)

	if err := Upsert(path, "FOO", "new"); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(path, "NEW", "hello world"); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(path)
	got := string(b)

	if !strings.Contains(got, "FOO=new\n") {
		t.Errorf("expected FOO replaced, got:\n%s", got)
	}
	if !strings.Contains(got, `NEW="hello world"`) {
		t.Errorf("expected NEW quoted (has space), got:\n%s", got)
	}
	if !strings.Contains(got, "# header") {
		t.Errorf("comment should be preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "OTHER=keep") {
		t.Errorf("OTHER should be preserved, got:\n%s", got)
	}
}

func TestUpsert_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := Upsert(path, "K", "v"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "K=v\n" {
		t.Errorf("unexpected content: %q", b)
	}
}
