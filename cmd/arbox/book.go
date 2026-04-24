package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/lafofo-nivo/arbox-scheduler/internal/arboxapi"
	"github.com/lafofo-nivo/arbox-scheduler/internal/envfile"
)

// newBookCmd is the `arbox book` sub-tree.
//
//	arbox book class    --schedule-id N [--send]
//	arbox book cancel   --schedule-user-id N [--send]
//	arbox book waitlist join  --schedule-id N [--send]
//	arbox book waitlist leave --standby-id N  [--send]
//
// Default is --dry-run (no network mutation). --send must be opt-in. The
// mutation code also prints the exact URL + body so we can reproduce with curl.
func newBookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "book",
		Short: "Book, cancel, or manage waitlist entries",
		Long: `Write operations against the Arbox member API.

All commands are dry-run by default — they print exactly what would be sent
and make no network mutation. Use --send to actually perform the action.

Endpoints (from the member app):
  POST /api/v2/scheduleUser/insert  — confirmed book
  POST /api/v2/scheduleUser/cancel  — best guess cancel
  POST /api/v2/scheduleStandBy/insert — join waitlist
  POST /api/v2/scheduleStandBy/delete — leave waitlist`,
	}

	waitlist := &cobra.Command{
		Use:   "waitlist",
		Short: "Join or leave a class waitlist (standby)",
	}
	waitlist.AddCommand(newWaitlistJoinCmd(), newWaitlistLeaveCmd())

	cmd.AddCommand(newBookClassCmd(), newBookCancelCmd(), waitlist)
	return cmd
}

// -- book class -----------------------------------------------------------

func newBookClassCmd() *cobra.Command {
	var (
		scheduleID int
		send       bool
	)
	cmd := &cobra.Command{
		Use:   "class",
		Short: "Book a class by schedule_id (from `arbox classes list`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if scheduleID <= 0 {
				return fmt.Errorf("--schedule-id is required")
			}
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}
			membershipID, err := ensureMembershipUserID(cmd.Context(), client)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()

			res, err := client.BookClass(ctx, membershipID, scheduleID, !send)
			printMutation("book class", res, err)
			return err
		},
	}
	cmd.Flags().IntVar(&scheduleID, "schedule-id", 0, "schedule_id (column SCHEDULE_ID in `arbox classes list`)")
	cmd.Flags().BoolVar(&send, "send", false, "actually send the request (default dry-run)")
	return cmd
}

// -- book cancel ----------------------------------------------------------

func newBookCancelCmd() *cobra.Command {
	var (
		scheduleUserID int
		send           bool
	)
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel an existing booking by schedule_user_id (best-guess endpoint)",
		Long: `Cancels a booking.

NOTE: The cancel endpoint hasn't been confirmed yet — we're using the most
likely path /api/v2/scheduleUser/cancel. If the real Arbox app uses a
different path, --send will return a 4xx and the dry-run body will show us
what we tried.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if scheduleUserID <= 0 {
				return fmt.Errorf("--schedule-user-id is required")
			}
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()

			res, err := client.CancelBooking(ctx, scheduleUserID, !send)
			printMutation("cancel booking", res, err)
			return err
		},
	}
	cmd.Flags().IntVar(&scheduleUserID, "schedule-user-id", 0, "your schedule_user_id from the booked_users array")
	cmd.Flags().BoolVar(&send, "send", false, "actually send the request (default dry-run)")
	return cmd
}

// -- book waitlist join / leave -------------------------------------------

func newWaitlistJoinCmd() *cobra.Command {
	var (
		scheduleID int
		send       bool
	)
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join the standby list for a full class (best-guess endpoint)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if scheduleID <= 0 {
				return fmt.Errorf("--schedule-id is required")
			}
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}
			membershipID, err := ensureMembershipUserID(cmd.Context(), client)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()

			res, err := client.JoinWaitlist(ctx, membershipID, scheduleID, !send)
			printMutation("waitlist join", res, err)
			return err
		},
	}
	cmd.Flags().IntVar(&scheduleID, "schedule-id", 0, "schedule_id to join the waitlist for")
	cmd.Flags().BoolVar(&send, "send", false, "actually send the request (default dry-run)")
	return cmd
}

func newWaitlistLeaveCmd() *cobra.Command {
	var (
		standbyID int
		send      bool
	)
	cmd := &cobra.Command{
		Use:   "leave",
		Short: "Leave a standby list by standby_id (best-guess endpoint)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if standbyID <= 0 {
				return fmt.Errorf("--standby-id is required")
			}
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()

			res, err := client.LeaveWaitlist(ctx, standbyID, !send)
			printMutation("waitlist leave", res, err)
			return err
		},
	}
	cmd.Flags().IntVar(&standbyID, "standby-id", 0, "your user_in_standby id from the class payload")
	cmd.Flags().BoolVar(&send, "send", false, "actually send the request (default dry-run)")
	return cmd
}

// -- helpers --------------------------------------------------------------

// ensureMembershipUserID resolves ARBOX_MEMBERSHIP_USER_ID from env, or
// looks it up via /api/v2/boxes/<box_id>/memberships/1 and persists it.
func ensureMembershipUserID(ctx context.Context, client *arboxapi.Client) (int, error) {
	if s := os.Getenv("ARBOX_MEMBERSHIP_USER_ID"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n, nil
		}
	}
	boxIDStr := os.Getenv("ARBOX_BOX_ID")
	if boxIDStr == "" {
		return 0, fmt.Errorf("ARBOX_BOX_ID not set; run `arbox classes discover` first")
	}
	boxID, err := strconv.Atoi(boxIDStr)
	if err != nil {
		return 0, fmt.Errorf("bad ARBOX_BOX_ID=%q", boxIDStr)
	}
	m, err := client.GetMembership(ctx, boxID)
	if err != nil {
		return 0, err
	}
	_ = envfile.Upsert(defaultEnvPath(), "ARBOX_MEMBERSHIP_USER_ID", strconv.Itoa(m.ID))
	fmt.Fprintf(os.Stderr, "[membership] id=%d (%s, %s) saved to %s\n",
		m.ID, m.PlanName(), m.StatusLabel(), defaultEnvPath())
	return m.ID, nil
}

// printMutation formats a MutationResult for the user.
func printMutation(label string, res *arboxapi.MutationResult, err error) {
	if res == nil {
		fmt.Printf("[%s] no result (err=%v)\n", label, err)
		return
	}
	fmt.Printf("[%s]\n", label)
	fmt.Printf("  %s %s\n", res.Method, res.URL)
	fmt.Println("  request body:")
	for _, line := range splitLines(res.RequestJSON) {
		fmt.Println("    " + line)
	}
	if !res.Sent {
		fmt.Println("  (dry-run: not sent) — pass --send to execute")
		return
	}
	fmt.Printf("  status: %d\n", res.StatusCode)
	if res.Message != "" {
		fmt.Printf("  message: %s\n", res.Message)
	}
	if len(res.ResponseRaw) > 0 {
		fmt.Printf("  response: %s\n", truncate(string(res.ResponseRaw), 500))
	}
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	} else {
		fmt.Println("  result: OK")
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i, ch := range s {
		if ch == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
