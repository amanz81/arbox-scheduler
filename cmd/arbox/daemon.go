package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/notify"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

// Version is overridden at build time via `-ldflags "-X main.Version=vX.Y"`.
var Version = "dev"

// newDaemonCmd is the long-running process we ship to the production host
// (Oracle Free Tier VM today; anything Linux or a container works).
//
// For now it's a HEARTBEAT + RESOLVE daemon: every `--interval`, fetch the
// next N days of options and log what's coming, including the countdown to
// the next booking window. Good enough to verify the deploy pipeline and
// auto-relogin work end-to-end.
//
// The priority booking engine (waitlist two classes, book the winner,
// cancel the loser) will replace the heartbeat body in a follow-up change.
func newDaemonCmd() *cobra.Command {
	var (
		interval      time.Duration
		lookaheadDays int
	)
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Long-running supervisor (systemd / launchd / container entrypoint)",
		Long: `Runs the Arbox scheduler as a long-running process.

Current behavior (heartbeat-only, no real bookings yet):
  - Every --interval, log the next planned option and countdown.
  - Re-auths silently when the access token expires.
  - Exits cleanly on SIGINT / SIGTERM.
  - If TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID are set, sends Telegram:
      · one *online* message on boot
      · one *heartbeat* per calendar day (local TZ) with next-window summary
      · one *shutdown* message on SIGTERM
      · long-poll command handler: /start, /help, /status, /weeklyavailable, /setup, /setupdone, /setupcancel (setMyCommands)

Designed to be a container CMD or a systemd ExecStart.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadValidated()
			if err != nil {
				return err
			}
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}

			notifier, warns := notify.FromEnv()
			for _, w := range warns {
				fmt.Println("[notify]", w)
			}

			// Signal-aware context for clean shutdown.
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			fmt.Printf("[daemon] version=%s interval=%s lookahead=%dd tz=%s\n",
				Version, interval, lookaheadDays, cfg.Timezone)

			// Discover locations_box_id once up front.
			locID, err := ensureLocationsBoxID(ctx, client)
			if err != nil {
				return fmt.Errorf("initial locations discovery: %w", err)
			}

			loc := cfg.Location()
			// At most one Telegram heartbeat per local calendar day (not on the
			// same deploy minute as EventOnline — that message already proves life).
			lastHeartbeatDay := time.Now().In(loc).Format("2006-01-02")

			// First tick immediately (stdout always); fold summary into *online*.
			summary, err := tick(ctx, cfg, client, locID, lookaheadDays)
			if err != nil {
				fmt.Printf("[daemon] tick error: %v\n", err)
				_ = notifier.Notify(notify.Message{Event: notify.EventError, Text: err.Error()})
			}
			onlineText := fmt.Sprintf(
				"version `%s`\ninterval `%s`\nlookahead `%dd`\ntz `%s`",
				Version, interval.String(), lookaheadDays, cfg.Timezone)
			if summary != "" && err == nil {
				onlineText += "\n" + summary
			}
			if err := notifier.Notify(notify.Message{Event: notify.EventOnline, Text: onlineText}); err != nil {
				fmt.Printf("[notify] online message: %v\n", err)
			}

			if tok := os.Getenv("TELEGRAM_BOT_TOKEN"); tok != "" {
				if cid := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID")); cid != "" {
					chatID, err := strconv.ParseInt(cid, 10, 64)
					if err != nil {
						fmt.Printf("[telegram-bot] skip: TELEGRAM_CHAT_ID: %v\n", err)
					} else {
						go runTelegramCommandBot(ctx, tok, chatID, loadValidated, client, locID, lookaheadDays)
					}
				}
			}

			// Proactive scheduler: wakes precisely at every WindowOpen and
			// triggers runBooker so popular slots aren't missed by the 5-min
			// safety-net polling.
			go runProactiveBooker(ctx, loadValidated, client, notifier, locID, lookaheadDays)

			// HTTP API for LLM agents (nanobot / Claude / OpenAI tool-calling).
			// No-op when ARBOX_API_*_TOKEN env vars are unset.
			go runHTTPAPI(ctx, loadValidated, client, locID, lookaheadDays)

			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					fmt.Println("[daemon] shutdown requested — goodbye")
					_ = notifier.Notify(notify.Message{
						Event: notify.EventShutdown,
						Text:  "received SIGINT/SIGTERM",
					})
					return nil
				case <-ticker.C:
					cfg2, err := loadValidated()
					if err != nil {
						fmt.Printf("[daemon] reload config: %v\n", err)
						_ = notifier.Notify(notify.Message{Event: notify.EventError, Text: "reload config: " + err.Error()})
						continue
					}
					cfg = cfg2
					ps, _ := readPauseState()
					nowTick := time.Now().In(loc)
					if ps.IsActive(nowTick) {
						fmt.Printf("[tick] %s SKIPPED — %s\n",
							nowTick.Format("2006-01-02 15:04:05 MST"),
							ps.Summary(nowTick, loc))
						maybeDailyHeartbeat(notifier, loc, &lastHeartbeatDay,
							"paused · "+ps.Summary(nowTick, loc),
							cfg, client, locID, lookaheadDays)
						continue
					}
					summary, err := tick(ctx, cfg, client, locID, lookaheadDays)
					if err != nil {
						fmt.Printf("[daemon] tick error: %v\n", err)
						_ = notifier.Notify(notify.Message{Event: notify.EventError, Text: err.Error()})
						continue
					}
					bookerMu.Lock()
					bookSummary, berr := runBooker(ctx, cfg, client, notifier, locID, lookaheadDays, nowTick)
					bookerMu.Unlock()
					if berr != nil {
						fmt.Printf("[booker] %v\n", berr)
						_ = notifier.Notify(notify.Message{Event: notify.EventError, Text: "booker: " + berr.Error()})
					} else if bookSummary != "" {
						fmt.Printf("[booker]\n%s\n", bookSummary)
						summary = strings.TrimSpace(summary + "\n" + bookSummary)
					}
					maybeDailyHeartbeat(notifier, loc, &lastHeartbeatDay, summary, cfg, client, locID, lookaheadDays)
				}
			}
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Minute,
		"how often to re-resolve the schedule")
	cmd.Flags().IntVar(&lookaheadDays, "lookahead", 7,
		"days to look ahead when resolving options")
	return cmd
}

// maybeDailyHeartbeat sends at most one EventHeartbeat per local calendar day.
// Heartbeat body now includes:
//   * the tick summary (alive · next window …)
//   * /selftest results (✓/✗ per check)
//   * next 3 planned bookings (date · category list · window · time-to-window)
func maybeDailyHeartbeat(
	n notify.Notifier,
	loc *time.Location,
	lastDay *string,
	summary string,
	cfg *config.Config,
	client *arboxapi.Client,
	locID, days int,
) {
	now := time.Now().In(loc)
	day := now.Format("2006-01-02")
	if day == *lastDay {
		return
	}
	*lastDay = day
	if summary == "" {
		summary = "no summary"
	}
	body := summary

	// Self-test (best-effort; we still send the heartbeat if it errors).
	if checks := runSelfTest(context.Background(), cfg, client, locID, days); len(checks) > 0 {
		body += "\n\n" + formatSelfTestReport(checks)
	}
	if next := nextPlannedBookingsSummary(cfg, days, 3); len(next) > 0 {
		body += "\nNext scheduled bookings:\n"
		for _, l := range next {
			body += "  · " + l + "\n"
		}
	}

	_ = n.Notify(notify.Message{
		Event: notify.EventHeartbeat,
		Text:  body,
		When:  now,
	})
}

// tick runs one heartbeat iteration: resolve planned options, print the
// next window countdown. Uses `client` to exercise the auth path so an
// expired token will trigger silent re-login here rather than at booking
// time.
//
// summary is a single-line MarkdownV2-safe plain string (gets escaped by
// the notifier) suitable for the daily Telegram heartbeat.
func tick(ctx context.Context, cfg *config.Config, client *arboxapi.Client, locID, days int) (summary string, err error) {
	loc := cfg.Location()
	now := time.Now().In(loc)
	fmt.Printf("[tick] %s  locations_box_id=%d\n",
		now.Format("2006-01-02 15:04:05 MST"), locID)

	opts, err := schedule.NextOptions(cfg, now, days)
	if err != nil {
		return "", fmt.Errorf("resolve options: %w", err)
	}
	if len(opts) == 0 {
		fmt.Printf("  (no planned options in the next %d days)\n", days)
		return fmt.Sprintf("alive · no planned options in the next %d days", days), nil
	}

	// Fetch today's classes just to exercise the auth path and catch a
	// stale token early.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, err = client.GetScheduleDay(fetchCtx, today, locID)
	cancel()
	if err != nil {
		fmt.Printf("  auth-probe fetch failed: %v\n", err)
	}

	// Find the next option whose window is still in the future.
	var next *schedule.PlannedOption
	for i := range opts {
		if opts[i].WindowOpen.After(now) {
			next = &opts[i]
			break
		}
	}
	if next == nil {
		fmt.Println("  (all lookahead windows are already open)")
		return "alive · all booking windows in lookahead are already open", nil
	}
	fmt.Printf("  next window opens in %s @ %s — %s %s (pri=%d, cat=%q)\n",
		next.WindowOpen.Sub(now).Round(time.Second),
		next.WindowOpen.Format("2006-01-02 15:04 MST"),
		next.Weekday, next.Time, next.Priority, next.Category)

	summary = fmt.Sprintf(
		"alive · next in %s · window %s · %s %s · pri %d · %s",
		next.WindowOpen.Sub(now).Round(time.Second),
		next.WindowOpen.Format("Mon 02 Jan 15:04"),
		next.Weekday, next.Time,
		next.Priority, next.Category)
	return summary, nil
}
