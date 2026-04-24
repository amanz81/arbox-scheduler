package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/lafofo-nivo/arbox-scheduler/internal/arboxapi"
	"github.com/lafofo-nivo/arbox-scheduler/internal/envfile"
)

// defaultEnvPathFallback is the local-dev default. On systemd / containers the
// real path comes from envfile.Path which checks ARBOX_ENV_FILE first.
const defaultEnvPathFallback = ".env"

// defaultEnvPath returns the current .env path (respects ARBOX_ENV_FILE).
// Kept as a function so it picks up env changes after init, not just at start.
func defaultEnvPath() string { return envfile.Path(defaultEnvPathFallback) }

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage Arbox credentials and tokens",
	}
	cmd.AddCommand(newAuthLoginCmd(), newAuthWhoamiCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	var save bool
	var envPath string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to Arbox and obtain a session token",
		Long: `Log in to Arbox using email + password and obtain a JWT session token.

Credentials are read from (in order):
  1. environment variables ARBOX_EMAIL / ARBOX_PASSWORD
  2. the .env file (same vars)
  3. interactive prompt (password hidden)

The token is printed and, unless --save=false, also written to .env as
ARBOX_TOKEN=... . .env is gitignored and never committed.

This endpoint is reverse-engineered and unofficial; see internal/arboxapi.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = envfile.Load(envPath)

			email := strings.TrimSpace(os.Getenv("ARBOX_EMAIL"))
			password := os.Getenv("ARBOX_PASSWORD")

			var err error
			if email == "" {
				email, err = promptLine("Arbox email: ")
				if err != nil {
					return err
				}
			}
			if password == "" {
				password, err = promptSecret("Arbox password: ")
				if err != nil {
					return err
				}
			}
			if email == "" || password == "" {
				return errors.New("email and password are required")
			}

			baseURL := os.Getenv("ARBOX_BASE_URL")
			client := arboxapi.New(baseURL)

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			resp, err := client.LoginAndStore(ctx, email, password)
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}

			fmt.Println("login OK")
			if resp.FirstName != "" || resp.LastName != "" {
				fmt.Printf("user: %s %s (id=%d)\n", resp.FirstName, resp.LastName, resp.UserID)
			} else if resp.UserID != 0 {
				fmt.Printf("user id: %d\n", resp.UserID)
			}
			fmt.Printf("access token:  %s\n", maskToken(resp.Token))
			fmt.Printf("refresh token: %s\n", maskToken(resp.RefreshToken))

			// Try to surface box id if present in the raw response.
			if boxID := findInt(resp.Raw, "box_fk", "boxFk", "boxId", "box_id"); boxID != 0 {
				fmt.Printf("detected box id: %d\n", boxID)
			}
			if showResp, _ := cmd.Flags().GetBool("show-response"); showResp {
				b, _ := json.MarshalIndent(resp.Raw, "", "  ")
				// Do not leak the tokens even in debug output.
				redacted := redactRawTokens(b)
				fmt.Println("raw response (tokens redacted):")
				fmt.Println(string(redacted))
			}

			if save {
				if err := envfile.Upsert(envPath, "ARBOX_TOKEN", resp.Token); err != nil {
					return fmt.Errorf("save token to %s: %w", envPath, err)
				}
				if resp.RefreshToken != "" {
					if err := envfile.Upsert(envPath, "ARBOX_REFRESH_TOKEN", resp.RefreshToken); err != nil {
						return fmt.Errorf("save refresh token to %s: %w", envPath, err)
					}
				}
				// Persist email if the user typed it but didn't have it in
				// .env yet. Password stays where they put it (never written
				// by us unless already there).
				if os.Getenv("ARBOX_EMAIL") == "" {
					_ = envfile.Upsert(envPath, "ARBOX_EMAIL", email)
				}
				fmt.Printf("saved ARBOX_TOKEN + ARBOX_REFRESH_TOKEN to %s\n", envPath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&save, "save", true, "save the token to .env (ARBOX_TOKEN=...)")
	cmd.Flags().StringVar(&envPath, "env-file", defaultEnvPath(), ".env file to read from / write to")
	cmd.Flags().Bool("show-response", false, "print the full raw login response (debugging)")
	return cmd
}

func newAuthWhoamiCmd() *cobra.Command {
	var envPath string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the currently stored Arbox token (masked) and verify it's non-empty",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = envfile.Load(envPath)
			token := os.Getenv("ARBOX_TOKEN")
			refresh := os.Getenv("ARBOX_REFRESH_TOKEN")
			email := os.Getenv("ARBOX_EMAIL")
			if token == "" {
				return errors.New("no ARBOX_TOKEN set; run `arbox auth login`")
			}
			fmt.Printf("email:         %s\n", email)
			fmt.Printf("access token:  %s\n", maskToken(token))
			fmt.Printf("refresh token: %s\n", maskToken(refresh))
			return nil
		},
	}
	cmd.Flags().StringVar(&envPath, "env-file", defaultEnvPath(), ".env file to read")
	return cmd
}

// promptLine reads a line from stdin. Trims trailing newline and whitespace.
func promptLine(prompt string) (string, error) {
	fmt.Print(prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptSecret reads a password without echoing it to the terminal.
func promptSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// redactRawTokens replaces any "token" / "refreshToken" string value in the
// marshaled JSON with "<REDACTED>" so users can safely paste --show-response
// output when asking for help.
func redactRawTokens(b []byte) []byte {
	s := string(b)
	for _, key := range []string{`"token"`, `"refreshToken"`, `"accessToken"`} {
		i := 0
		for {
			idx := strings.Index(s[i:], key)
			if idx < 0 {
				break
			}
			idx += i
			// Find the next string value after the colon.
			colon := strings.Index(s[idx:], ":")
			if colon < 0 {
				break
			}
			start := idx + colon + 1
			// Skip whitespace.
			for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n') {
				start++
			}
			if start >= len(s) || s[start] != '"' {
				i = idx + len(key)
				continue
			}
			// Find the closing quote (not a backslash-escape).
			end := start + 1
			for end < len(s) && s[end] != '"' {
				if s[end] == '\\' && end+1 < len(s) {
					end += 2
					continue
				}
				end++
			}
			if end >= len(s) {
				break
			}
			s = s[:start] + `"<REDACTED>"` + s[end+1:]
			i = start + len(`"<REDACTED>"`)
		}
	}
	return []byte(s)
}

// maskToken returns "<first6>...<last4> (len=N)" so logs stay useful without
// leaking the whole JWT.
func maskToken(t string) string {
	if t == "" {
		return "<none>"
	}
	if len(t) <= 12 {
		return fmt.Sprintf("<%d chars>", len(t))
	}
	return fmt.Sprintf("%s...%s (len=%d)", t[:6], t[len(t)-4:], len(t))
}

// findInt looks for the first numeric value under any of the given keys,
// walking into nested maps when it encounters them.
func findInt(raw map[string]any, keys ...string) int {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var walk func(v any) int
	walk = func(v any) int {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				if want[k] {
					switch n := vv.(type) {
					case float64:
						if n != 0 {
							return int(n)
						}
					case string:
						if n != "" {
							var i int
							fmt.Sscanf(n, "%d", &i)
							if i != 0 {
								return i
							}
						}
					}
				}
			}
			for _, vv := range x {
				if got := walk(vv); got != 0 {
					return got
				}
			}
		case []any:
			for _, vv := range x {
				if got := walk(vv); got != 0 {
					return got
				}
			}
		}
		return 0
	}
	return walk(raw)
}
