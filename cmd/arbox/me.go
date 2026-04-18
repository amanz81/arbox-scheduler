package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// newMeCmd prints the authenticated user's identity + membership + monthly
// usage. Handy first-run sanity check on a fresh VPS ("who am I logged in as?").
func newMeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Print your Arbox identity, membership, and monthly quota usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Println("identity:")
			fmt.Println("  email:", os.Getenv("ARBOX_EMAIL"))
			if t := os.Getenv("ARBOX_TOKEN"); len(t) > 14 {
				fmt.Printf("  token: %s...%s (len=%d)\n", t[:6], t[len(t)-4:], len(t))
			}

			// Membership.
			boxIDStr := os.Getenv("ARBOX_BOX_ID")
			if boxIDStr != "" {
				if boxID, err := strconv.Atoi(boxIDStr); err == nil {
					ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
					m, err := client.GetMembership(ctx, boxID)
					cancel()
					if err != nil {
						fmt.Printf("membership: error: %v\n", err)
					} else {
						fmt.Println("membership:")
						fmt.Printf("  id:            %d\n", m.ID)
						fmt.Printf("  plan:          %s\n", m.PlanName())
						fmt.Printf("  status:        %s\n", m.StatusLabel())
						end := "(ongoing)"
						if m.End != nil {
							end = *m.End
						}
						fmt.Printf("  period:        %s -> %s\n", m.Start, end)
						if m.SessionsLeft != nil {
							fmt.Printf("  sessions left: %d\n", *m.SessionsLeft)
						}
					}
				}
			} else {
				fmt.Println("membership: (ARBOX_BOX_ID not set — run `arbox classes discover`)")
			}

			// Feed (quota).
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			f, err := client.GetFeed(ctx)
			cancel()
			if err != nil {
				fmt.Printf("feed: error: %v\n", err)
			} else {
				past := f.PastBookings()
				future := f.FutureBookings()
				fmt.Println("feed:")
				fmt.Printf("  past bookings (this period):    %d\n", past)
				fmt.Printf("  future bookings:                %d\n", future)
				fmt.Printf("  total registered (this period): %d\n", past+future)
			}
			return nil
		},
	}
}
