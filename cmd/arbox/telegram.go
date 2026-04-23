package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newTelegramCmd groups Telegram helper commands (discover chat id, etc.).
func newTelegramCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "telegram",
		Short: "Telegram bot helpers (not a chat server — outbound notify only)",
	}
	root.AddCommand(newTelegramDiscoverCmd())
	return root
}

// newTelegramDiscoverCmd prints chat_id values seen in recent getUpdates.
// After you open @YourBot and tap Start, run this with TELEGRAM_BOT_TOKEN
// set (or in .env) and paste the printed chat_id into your host's secret store (.env / systemd EnvironmentFile / etc.) as
// TELEGRAM_CHAT_ID.
func newTelegramDiscoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "discover",
		Short: "List chat_id values from recent Telegram updates (run after /start in the bot)",
		RunE: func(cmd *cobra.Command, args []string) error {
			tok := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
			if tok == "" {
				return fmt.Errorf("set TELEGRAM_BOT_TOKEN in the environment or .env")
			}
			url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", tok)
			ctx := cmd.Context()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("telegram HTTP %d: %s", resp.StatusCode, string(body))
			}
			var parsed struct {
				OK     bool `json:"ok"`
				Result []struct {
					Message struct {
						Chat struct {
							ID        int64  `json:"id"`
							Type      string `json:"type"`
							Username  string `json:"username"`
							FirstName string `json:"first_name"`
						} `json:"chat"`
					} `json:"message"`
				} `json:"result"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return fmt.Errorf("parse telegram json: %w", err)
			}
			if !parsed.OK {
				return fmt.Errorf("telegram ok=false")
			}
			if len(parsed.Result) == 0 {
				fmt.Println("No updates yet.")
				fmt.Println("1) Open your bot in Telegram and tap Start (or send any message).")
				fmt.Println("2) Run this command again.")
				return nil
			}
			seen := make(map[int64]struct{})
			for _, u := range parsed.Result {
				c := u.Message.Chat
				if c.ID == 0 {
					continue
				}
				if _, ok := seen[c.ID]; ok {
					continue
				}
				seen[c.ID] = struct{}{}
				user := c.Username
				if user == "" {
					user = "?"
				}
				fmt.Printf("chat_id=%d  type=%s  @%s  name=%s\n",
					c.ID, c.Type, user, c.FirstName)
			}
			fmt.Println()
			fmt.Println("Add to your .env / host secret store:")
			for id := range seen {
				fmt.Printf("  TELEGRAM_CHAT_ID=%d\n", id)
			}
			return nil
		},
	}
}
