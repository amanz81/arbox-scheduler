// Package notify provides a pluggable notification interface.
// Phase 1 ships a stdout implementation; Phase 2+ can add Telegram, email, etc.
package notify

import (
	"fmt"
	"io"
	"os"
	"time"
)

// Event is the kind of booking event we're notifying about.
type Event string

const (
	EventBooked      Event = "booked"
	EventWaitlisted  Event = "waitlisted"
	EventFailed      Event = "failed"
	EventWindowOpens Event = "window-opens"
)

// Message is a single notification payload.
type Message struct {
	Event      Event
	When       time.Time // event time (local)
	ClassStart time.Time // class the event is about (local); zero if N/A
	Text       string    // human-readable text
}

// Notifier sends a Message somewhere. Implementations must be safe for
// concurrent use if they'll ever be called from the future daemon.
type Notifier interface {
	Notify(msg Message) error
}

// Stdout prints messages to an io.Writer (defaults to os.Stdout). It's the
// Phase 1 implementation and handy for dry-runs.
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
