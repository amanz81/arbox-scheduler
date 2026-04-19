package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

// newSelfTestCmd is `arbox selftest`: prints health checks for the local
// machine + Arbox API + plan, then up to 3 next planned bookings.
func newSelfTestCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "selftest",
		Short: "Verify auth, gym, plan, and show the next scheduled bookings",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadValidated()
			if err != nil {
				return err
			}
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}
			locID, err := ensureLocationsBoxID(cmd.Context(), client)
			if err != nil {
				return err
			}
			checks := runSelfTest(cmd.Context(), cfg, client, locID, days)
			fmt.Println(formatSelfTestReport(checks))
			if next := nextPlannedBookingsSummary(cfg, days, 3); len(next) > 0 {
				fmt.Println("Next scheduled bookings:")
				for _, l := range next {
					fmt.Println("  · " + l)
				}
			}
			for _, c := range checks {
				if !c.OK {
					return fmt.Errorf("self-test failed: %s", c.Name)
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "lookahead window for plan checks")
	return cmd
}

// selfCheck is one independent health probe.
type selfCheck struct {
	Name    string
	OK      bool
	Detail  string
	Latency time.Duration
}

// runSelfTest exercises the moving parts the daemon relies on so users can
// verify a deploy with one command.
//
// Checks (in order):
//  1. Config + timezone
//  2. Pause state (warn-only)
//  3. Locations / box discovery (auth + correct gym)
//  4. Membership lookup (needed for booking)
//  5. Schedule fetch for today (auth round-trip + TZ correctness)
//  6. Booking attempts file is read/writeable
//  7. Plan resolves at least one upcoming option for the next `days`
func runSelfTest(ctx context.Context, cfg *config.Config, client *arboxapi.Client, locID, days int) []selfCheck {
	var out []selfCheck
	add := func(name string, fn func() (string, error)) {
		t0 := time.Now()
		detail, err := fn()
		c := selfCheck{Name: name, Latency: time.Since(t0)}
		if err != nil {
			c.OK = false
			c.Detail = err.Error()
		} else {
			c.OK = true
			c.Detail = detail
		}
		out = append(out, c)
	}

	add("Config + TZ", func() (string, error) {
		loc := cfg.Location()
		if loc == nil {
			return "", fmt.Errorf("no location resolved for %q", cfg.Timezone)
		}
		return fmt.Sprintf("tz=%s now=%s", cfg.Timezone, time.Now().In(loc).Format("Mon 02 Jan 15:04 MST")), nil
	})

	add("Pause state", func() (string, error) {
		ps, err := readPauseState()
		if err != nil {
			return "", err
		}
		if tag := ps.Summary(time.Now().In(cfg.Location()), cfg.Location()); tag != "" {
			return "ACTIVE — " + tag, nil
		}
		return "off", nil
	})

	add("Locations API (auth + gym)", func() (string, error) {
		fctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		locs, err := client.GetLocations(fctx)
		if err != nil {
			return "", err
		}
		var matched string
		gym := strings.ToLower(strings.TrimSpace(cfg.Gym))
		for _, b := range locs {
			for _, l := range b.LocationsBox {
				if l.ID == locID {
					matched = b.BoxName + " / " + l.Name
				}
			}
		}
		if matched == "" {
			return "", fmt.Errorf("locations_box_id=%d not found in %d boxes", locID, len(locs))
		}
		if gym != "" && !strings.Contains(strings.ToLower(matched), gym) {
			return "", fmt.Errorf("active box %q does not contain config.gym %q", matched, cfg.Gym)
		}
		return matched + fmt.Sprintf(" (locations_box_id=%d)", locID), nil
	})

	add("Membership lookup", func() (string, error) {
		fctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		mid, err := ensureMembershipUserID(fctx, client)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("membership_user_id=%d", mid), nil
	})

	add("Schedule fetch (today)", func() (string, error) {
		loc := cfg.Location()
		now := time.Now().In(loc)
		fctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		cls, err := client.GetScheduleDay(fctx, now, locID)
		if err != nil {
			return "", err
		}
		you := 0
		for _, c := range cls {
			if c.YouStatus() != "" {
				you++
			}
		}
		return fmt.Sprintf("%d classes for %s (%d marked you BOOKED/WAITLIST)",
			len(cls), now.Format("2006-01-02"), you), nil
	})

	add("Attempts file", func() (string, error) {
		path := bookingAttemptsPath()
		// Read (must not error)
		_ = readAttemptsState()
		// Touch test: write the same content back to ensure path is writable.
		s := readAttemptsState()
		if err := writeAttemptsState(s); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		// Stat for size.
		fi, err := os.Stat(path)
		size := int64(0)
		if err == nil {
			size = fi.Size()
		}
		return fmt.Sprintf("%s (%d bytes, %d records)", path, size, len(s.Attempts)), nil
	})

	add("Plan resolves upcoming options", func() (string, error) {
		opts, err := schedule.NextOptions(cfg, time.Now().In(cfg.Location()), days)
		if err != nil {
			return "", err
		}
		if len(opts) == 0 {
			return "", fmt.Errorf("no PlannedOptions in next %d days", days)
		}
		return fmt.Sprintf("%d options across %d days", len(opts), days), nil
	})

	return out
}

// formatSelfTestReport renders a /selftest result as plain text.
func formatSelfTestReport(checks []selfCheck) string {
	var b strings.Builder
	pass, fail := 0, 0
	for _, c := range checks {
		if c.OK {
			pass++
		} else {
			fail++
		}
	}
	fmt.Fprintf(&b, "Self-test: %d passed, %d failed\n\n", pass, fail)
	for _, c := range checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s %s [%s] %s\n",
			mark, c.Name, c.Latency.Round(time.Millisecond), c.Detail)
	}
	return b.String()
}

// nextPlannedBookingsSummary returns up to `count` short lines describing the
// next planned booking actions (one per ClassStart). Used in the daily
// heartbeat so you can verify the daemon knows what it should do next.
func nextPlannedBookingsSummary(cfg *config.Config, days, count int) []string {
	opts, err := schedule.NextOptions(cfg, time.Now().In(cfg.Location()), days)
	if err != nil || len(opts) == 0 {
		return nil
	}
	// Group by ClassStart so we collapse priorities (Hall B then Hall A) into
	// one line per slot.
	type group struct {
		start  time.Time
		window time.Time
		opts   []schedule.PlannedOption
	}
	seen := map[time.Time]int{}
	var groups []group
	for _, o := range opts {
		if i, ok := seen[o.ClassStart]; ok {
			groups[i].opts = append(groups[i].opts, o)
			continue
		}
		seen[o.ClassStart] = len(groups)
		groups = append(groups, group{start: o.ClassStart, window: o.WindowOpen, opts: []schedule.PlannedOption{o}})
	}

	now := time.Now().In(cfg.Location())
	var lines []string
	for _, g := range groups {
		if !g.start.After(now) {
			continue
		}
		// Compose category list "Hall B then Hall A" (priority order).
		var names []string
		for _, o := range g.opts {
			n := strings.TrimSpace(o.Category)
			if n == "" {
				n = "(filter)"
			}
			names = append(names, n)
		}
		var dur string
		if g.window.After(now) {
			dur = "in " + shortDuration(g.window.Sub(now).Round(time.Minute))
		} else {
			dur = "WINDOW OPEN NOW"
		}
		lines = append(lines, fmt.Sprintf("%s — %s · window %s (%s)",
			g.start.Format("Mon 02 Jan 15:04"),
			strings.Join(names, " then "),
			g.window.Format("Mon 02 Jan 15:04"),
			dur))
		if len(lines) >= count {
			break
		}
	}
	return lines
}
