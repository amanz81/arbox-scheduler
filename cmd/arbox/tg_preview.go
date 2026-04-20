package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
)

// newTgPreviewCmd is `arbox tg-preview <command> [args]`: prints the exact
// text body the Telegram bot would send for a given slash command, so you
// can test renderer changes without going through Telegram. It calls the
// same build*Report functions the bot's switch in telegram_bot.go calls.
//
// Examples:
//
//	arbox tg-preview /status
//	arbox tg-preview /morning 6-12 week
//	arbox tg-preview /evening 16-22 2
//	arbox tg-preview /selftest
//	arbox tg-preview /version
//	arbox tg-preview /help
//
// The output is the *body* only (no "*Status*" markdown wrapper) since the
// wrapper and MarkdownV2 escaping live in the Telegram send path and would
// only obscure the content when you're reading it in a terminal.
func newTgPreviewCmd() *cobra.Command {
	var lookahead int
	cmd := &cobra.Command{
		Use:   "tg-preview <command> [args]",
		Short: "Print what the Telegram bot would send for a given slash command",
		Long: "Dispatches to the same build*Report functions the Telegram " +
			"command bot uses, so you can verify the exact text without " +
			"sending a real message.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTgPreview(cmd.Context(), args, lookahead)
		},
	}
	cmd.Flags().IntVar(&lookahead, "lookahead", 7, "lookahead window in days (mirrors daemon --lookahead)")
	return cmd
}

func runTgPreview(ctx context.Context, args []string, lookahead int) error {
	cmdName := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(args[0])), "/")
	cmdArgs := args[1:]

	switch cmdName {
	case "help", "start":
		fmt.Println(helpTelegramBody())
		return nil

	case "version":
		cfg, client, locID, err := tgPreviewBootstrap(ctx)
		if err != nil {
			return err
		}
		_ = client
		fmt.Println(buildVersionReport(cfg, locID, lookahead))
		return nil

	case "selftest":
		cfg, client, locID, err := tgPreviewBootstrap(ctx)
		if err != nil {
			return err
		}
		checks := runSelfTest(ctx, cfg, client, locID, lookahead)
		body := formatSelfTestReport(checks)
		if next := nextPlannedBookingsSummary(cfg, lookahead, 3); len(next) > 0 {
			body += "\nNext scheduled bookings:\n"
			for _, l := range next {
				body += "  · " + l + "\n"
			}
		}
		fmt.Print(body)
		return nil

	case "status":
		cfg, client, locID, err := tgPreviewBootstrap(ctx)
		if err != nil {
			return err
		}
		rep, err := buildStatusShortReport(ctx, cfg, client, locID, lookahead)
		if err != nil {
			return err
		}
		fmt.Print(rep)
		return nil

	case "morning":
		return tgPreviewWindowCmd(ctx, cmdArgs, 6, 12)

	case "evening":
		return tgPreviewWindowCmd(ctx, cmdArgs, 16, 22)

	default:
		return fmt.Errorf("unsupported command %q. supported: /status, /morning, /evening, /selftest, /version, /help", "/"+cmdName)
	}
}

func tgPreviewWindowCmd(ctx context.Context, args []string, defStart, defEnd int) error {
	startH, endH, days, err := parseMorningArgs(args, defStart, defEnd, 1)
	if err != nil {
		return fmt.Errorf("%v\nUsage: /morning|/evening [HH-HH] [days|week]", err)
	}
	cfg, client, locID, err := tgPreviewBootstrap(ctx)
	if err != nil {
		return err
	}
	rep, err := buildClassWindowReport(ctx, cfg, client, locID, startH, endH, days)
	if err != nil {
		return err
	}
	fmt.Print(rep)
	return nil
}

// tgPreviewBootstrap runs the same auth + location discovery the daemon does
// on boot, so preview output matches what the live bot would produce.
func tgPreviewBootstrap(ctx context.Context) (*config.Config, *arboxapi.Client, int, error) {
	c, err := loadValidated()
	if err != nil {
		return nil, nil, 0, err
	}
	cl, _, err := newAuthedClient(ctx)
	if err != nil {
		return nil, nil, 0, err
	}
	lid, err := ensureLocationsBoxID(ctx, cl)
	if err != nil {
		return nil, nil, 0, err
	}
	return c, cl, lid, nil
}
