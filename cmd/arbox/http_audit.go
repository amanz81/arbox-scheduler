package main

// Append-only audit log for the HTTP API.
//
// One JSON line per *mutation request* (regardless of dry-run vs confirm).
// File lives at /data/audit.jsonl by default; ARBOX_AUDIT_LOG overrides.
// When the file exceeds 10 MB it is renamed to "<path>.1" (single backup,
// previous .1 is overwritten) and a fresh file is started. Mode 0o600.
//
// The /api/v1/audit endpoint reads this file as a tail (newest first). We
// avoid loading the whole file just to read the last N lines: we open
// once, read everything (capped at the rotation size), split by newlines,
// and reverse-iterate — simple and fast enough for 10 MB.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const auditMaxBytes = 10 * 1024 * 1024 // 10 MB before rotation.

// auditEntry is the on-disk shape of one audited mutation. ts is RFC3339 in
// UTC so consumers don't have to deal with offsets when sorting.
type auditEntry struct {
	TS         string         `json:"ts"`
	Route      string         `json:"route"`
	TokenKind  string         `json:"token_kind"`
	Args       map[string]any `json:"args,omitempty"`
	Confirm    bool           `json:"confirm"`
	Result     string         `json:"result,omitempty"`
	ScheduleID int            `json:"schedule_id,omitempty"`
	LatencyMs  int64          `json:"latency_ms,omitempty"`
	ClientIP   string         `json:"client_ip,omitempty"`
	HTTPStatus int            `json:"http_status,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// auditLogPath returns the audit file location, honoring ARBOX_AUDIT_LOG.
// Defaults next to the other state files (so locally it's the repo root,
// on Fly it's /data).
func auditLogPath() string {
	if v := strings.TrimSpace(os.Getenv("ARBOX_AUDIT_LOG")); v != "" {
		return v
	}
	return filepath.Join(dataDir(), "audit.jsonl")
}

// auditLog wraps a single file with a mutex; rotation happens in-line on
// the writer's path so callers don't have to think about it.
type auditLog struct {
	mu sync.Mutex
}

var globalAuditLog = &auditLog{}

// appendOne serializes the entry and writes one JSON line. Errors are
// surfaced to the caller but never block the HTTP response — handlers log
// the error and carry on.
func (a *auditLog) appendOne(e auditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	path := auditLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	if fi, err := os.Stat(path); err == nil && fi.Size() >= auditMaxBytes {
		_ = os.Rename(path, path+".1")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

// readTail returns up to `limit` audit entries, newest first, optionally
// filtered to `since` (entries with ts >= since).
func (a *auditLog) readTail(limit int, since time.Time) ([]auditEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}
	path := auditLogPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	out := make([]auditEntry, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var e auditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if !since.IsZero() {
			ts, terr := time.Parse(time.RFC3339, e.TS)
			if terr == nil && ts.Before(since) {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// clientIP returns the best-effort client IP. On Fly, Fly-Client-IP is the
// real edge; otherwise we fall back to the connection's RemoteAddr.
func clientIP(r *http.Request) string {
	if s := strings.TrimSpace(r.Header.Get("Fly-Client-IP")); s != "" {
		return s
	}
	if s := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); s != "" {
		if i := strings.IndexByte(s, ','); i > 0 {
			return strings.TrimSpace(s[:i])
		}
		return s
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

// nowAuditTS returns RFC3339 UTC with millisecond precision (compact and
// sortable).
func nowAuditTS() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// fmtUnusedHelper exists only to anchor `fmt` usage if other helpers below
// are removed; keeps the import stable while we evolve this file.
var _ = fmt.Sprint
