// Package envfile is a tiny loader/updater for a .env file.
//
// Design goals:
//   - No external dep (keeps the binary lean).
//   - Load: only set env vars that are not already set (so real env wins).
//   - Upsert: preserve original file order, comments, and blank lines;
//     replace an existing key in place or append a new one.
package envfile

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// Path returns the effective .env path for the current process.
//
// Priority:
//  1. Explicit ARBOX_ENV_FILE env var (useful on Fly.io / systemd where we
//     want the file on a persistent volume like /data/.env).
//  2. The fallback provided by the caller (typically ".env" in cwd).
//
// Keeping this in one place so every caller (Load, Upsert, auth) agrees.
func Path(fallback string) string {
	if p := os.Getenv("ARBOX_ENV_FILE"); p != "" {
		return p
	}
	return fallback
}

// Load reads a .env file and sets os environment variables for any key that
// is not already set in the process environment. Missing file is not an error.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		key, val, ok := parseLine(s.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return s.Err()
}

// Upsert writes key=value to path, preserving other lines. If the key already
// exists, its line is replaced; otherwise a new line is appended. The file is
// created (0600) if it doesn't exist.
func Upsert(path, key, value string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var lines []string
	if len(existing) > 0 {
		lines = strings.Split(string(existing), "\n")
	}
	replaced := false
	for i, line := range lines {
		k, _, ok := parseLine(line)
		if !ok {
			continue
		}
		if k == key {
			lines[i] = fmt.Sprintf("%s=%s", key, quoteIfNeeded(value))
			replaced = true
			break
		}
	}
	if !replaced {
		// Ensure a trailing newline before appending if file was non-empty
		// and didn't end with one.
		if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
			lines = append(lines, "")
		}
		lines = append(lines, fmt.Sprintf("%s=%s", key, quoteIfNeeded(value)))
	}

	// Preserve a single trailing newline.
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o600)
}

// parseLine parses a single "KEY=value" line. It skips blanks and comments.
// Supports surrounding single or double quotes on the value.
func parseLine(line string) (key, value string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:eq])
	value = strings.TrimSpace(s[eq+1:])
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return key, value, key != ""
}

// quoteIfNeeded wraps value in double quotes if it contains whitespace or #.
func quoteIfNeeded(v string) string {
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, " \t#") {
		return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return v
}
