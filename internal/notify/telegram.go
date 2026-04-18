package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Telegram sends messages via the Telegram Bot API (`sendMessage`).
//
// No external deps — plain net/http. Uses parse_mode=MarkdownV2 per
// https://core.telegram.org/bots/api#markdownv2-style — fixed headers use
// *bold* / `code`; all dynamic text from Message is escaped via
// EscapeMarkdownV2 / escapeMarkdownV2Code so dots, parens, hyphens, etc.
// never break parsing.
//
// The Telegram API has strict rate limits (1 msg/sec per chat, 30 msg/sec
// across all chats). For this app that's not a constraint, but we still
// keep a small client timeout and one retry on 5xx to survive transient
// upstream blips without blocking the daemon.
type Telegram struct {
	Token    string // bot API token from @BotFather
	ChatID   string // numeric chat id (can be negative for channels/groups)
	Endpoint string // override for tests; "" -> https://api.telegram.org
	HTTP     *http.Client
}

// NewTelegram validates the inputs and returns a ready-to-use notifier.
// It does NOT hit the network — validity of the token/chat is discovered
// on the first Notify call.
func NewTelegram(token, chatID string) (*Telegram, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("telegram: empty bot token")
	}
	if _, err := strconv.ParseInt(chatID, 10, 64); err != nil {
		return nil, fmt.Errorf("telegram: chat id %q is not a valid integer: %w", chatID, err)
	}
	return &Telegram{
		Token:  token,
		ChatID: chatID,
		HTTP:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Notify formats and sends a single Message to the configured chat.
// Blocks up to ~20 s worst-case (two 10 s attempts including retry).
func (t *Telegram) Notify(msg Message) error {
	text := formatMessage(msg)
	return t.send(context.Background(), text)
}

// formatMessage builds a MarkdownV2 message: static titles use *bold*;
// dynamic body and timestamps are escaped.
func formatMessage(m Message) string {
	var body strings.Builder
	body.WriteString(headerLineFor(m.Event))
	if m.Text != "" {
		body.WriteString(EscapeMarkdownV2(m.Text))
	}
	if !m.ClassStart.IsZero() {
		body.WriteString("\nclass: `")
		body.WriteString(escapeMarkdownV2Code(m.ClassStart.Format("Mon 2006-01-02 15:04 MST")))
		body.WriteString("`")
	}
	return body.String()
}

// headerLineFor returns the first line (emoji + *title* + newline) in
// MarkdownV2. Title phrases are ASCII-only so no escaping inside *...*.
func headerLineFor(ev Event) string {
	switch ev {
	case EventBooked:
		return "✅ *Booked*\n"
	case EventWaitlisted:
		return "⏳ *Waitlisted*\n"
	case EventFailed:
		return "❌ *Booking failed*\n"
	case EventWindowOpens:
		return "⏰ *Window opening soon*\n"
	case EventOnline:
		return "🟢 *Arbox daemon online*\n"
	case EventShutdown:
		return "🔴 *Arbox daemon shutting down*\n"
	case EventError:
		return "⚠️ *Daemon error*\n"
	case EventHeartbeat:
		return "🫀 *Heartbeat*\n"
	case EventPreview:
		return "📅 *Weekly preview*\n"
	default:
		return "⚙️ `" + escapeMarkdownV2Code(string(ev)) + "`\n"
	}
}

// send POSTs to sendMessage with one retry on 5xx / network errors.
func (t *Telegram) send(ctx context.Context, text string) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			// Small, fixed backoff. We don't expect high contention.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
		err := t.doSend(ctx, text)
		if err == nil {
			return nil
		}
		lastErr = err
		// Only retry 5xx / network; don't retry 4xx (bad token, bad chat).
		if !isRetryable(err) {
			return err
		}
	}
	return lastErr
}

// doSend performs a single sendMessage HTTP call.
func (t *Telegram) doSend(ctx context.Context, text string) error {
	endpoint := t.Endpoint
	if endpoint == "" {
		endpoint = "https://api.telegram.org"
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", endpoint, t.Token)

	payload := map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return retryableErr{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return retryableErr{err: fmt.Errorf("telegram %d: %s", resp.StatusCode, string(body))}
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram %d: %s", resp.StatusCode, string(body))
	}

	// 2xx: Telegram wraps success in {"ok": true, ...}; if ok is false we
	// still want to surface the description.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var parsed struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &parsed)
	if !parsed.OK {
		return fmt.Errorf("telegram returned ok=false: %s", parsed.Description)
	}
	return nil
}

// retryableErr marks errors as safe to retry (network / 5xx).
type retryableErr struct{ err error }

func (e retryableErr) Error() string { return e.err.Error() }
func (e retryableErr) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	var r retryableErr
	return errors.As(err, &r)
}
