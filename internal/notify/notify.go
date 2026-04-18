// Package notify provides a pluggable notification interface.
//
// Phase 1 shipped only a stdout implementation. Phase 2 adds:
//   - Telegram (direct 1-way messages via Bot API, see telegram.go)
//   - Multi   (fan-out across several notifiers)
//   - FromEnv (constructs a sensible Notifier from TELEGRAM_* env vars,
//              falling back to stdout if they're unset)
package notify

import (
	"fmt"
	"io"
	"os"
	"time"
)

// Event categorizes what happened. Notifiers may format differently per event
// (e.g. Telegram adds emoji). Keep this small and close to business reality.
type Event string

const (
	// Booking-time events.
	EventBooked      Event = "booked"
	EventWaitlisted  Event = "waitlisted"
	EventFailed      Event = "failed"
	EventWindowOpens Event = "window-opens"

	// Lifecycle / ops events.
	EventOnline    Event = "online"    // daemon has booted
	EventShutdown  Event = "shutdown"  // daemon received SIGTERM/SIGINT
	EventError     Event = "error"     // unexpected runtime error
	EventHeartbeat Event = "heartbeat" // periodic "still alive" (daily-ish)
	EventPreview   Event = "preview"   // Saturday weekly preview
)

// Message is a single notification payload.
//
// ClassStart is optional and only meaningful for booking-time events
// (EventBooked/EventWaitlisted/EventFailed/EventWindowOpens).
type Message struct {
	Event      Event
	When       time.Time // event time (local); zero => now
	ClassStart time.Time // class the event is about (local); zero if N/A
	Text       string    // human-readable body
}

// Notifier sends a Message somewhere.
// Implementations MUST be safe for concurrent use from the daemon.
// Notify MUST NOT block longer than a few seconds; slow network calls
// should enforce their own timeouts.
type Notifier interface {
	Notify(msg Message) error
}

// ------------------------------------------------------------------ Stdout --

// Stdout prints messages to an io.Writer (defaults to os.Stdout).
// It's the lowest common denominator and always runs regardless of what
// other backends are configured, so `fly logs` is always the source of truth.
type Stdout struct {
	W io.Writer
}

func (s *Stdout) writer() io.Writer {
	if s.W != nil {
		return s.W
	}
	return os.Stdout
}

func (s *Stdout) Notify(msg Message) error {
	when := msg.When
	if when.IsZero() {
		when = time.Now()
	}
	_, err := fmt.Fprintf(s.writer(), "[%s] %s: %s\n",
		when.Format("2006-01-02 15:04:05 MST"), msg.Event, msg.Text)
	return err
}

// -------------------------------------------------------------------- Multi --

// Multi fans out a single Message to every backend.
// It always calls every backend regardless of individual errors, then
// returns the first error encountered (or nil). Useful so a Telegram outage
// never swallows the stdout log line.
type Multi struct {
	Backends []Notifier
}

func (m *Multi) Notify(msg Message) error {
	var first error
	for _, b := range m.Backends {
		if b == nil {
			continue
		}
		if err := b.Notify(msg); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ------------------------------------------------------------------ FromEnv --

// FromEnv builds the notifier stack based on environment variables:
//
//	TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID => adds Telegram notifier
//	(always present)                      => adds Stdout notifier
//
// Stdout is always first so it logs before we risk a network call.
// Returns a Notifier that is safe to call even if only stdout is available.
// Any parse error of the numeric chat id is reported via the `warn` slice
// so the daemon can log it but keep running.
func FromEnv() (n Notifier, warns []string) {
	backends := []Notifier{&Stdout{}}

	tok := os.Getenv("TELEGRAM_BOT_TOKEN")
	chat := os.Getenv("TELEGRAM_CHAT_ID")
	switch {
	case tok == "" && chat == "":
		// Neither set: quietly skip Telegram.
	case tok == "" || chat == "":
		warns = append(warns, "TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must both be set; Telegram notifier disabled")
	default:
		tg, err := NewTelegram(tok, chat)
		if err != nil {
			warns = append(warns, fmt.Sprintf("telegram notifier disabled: %v", err))
		} else {
			backends = append(backends, tg)
		}
	}

	if len(backends) == 1 {
		return backends[0], warns
	}
	return &Multi{Backends: backends}, warns
}
