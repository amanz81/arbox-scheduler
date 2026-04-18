package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/notify"
)

// runTelegramCommandBot registers slash commands with Telegram and long-polls
// getUpdates so /start, /help, and /status get replies. Only messages from
// allowedChatID are honored (same id as TELEGRAM_CHAT_ID).
//
// This runs in a goroutine; it exits when ctx is cancelled.
func runTelegramCommandBot(ctx context.Context, token string, allowedChatID int64, cfg *config.Config, client *arboxapi.Client, locID, lookaheadDays int) {
	base := "https://api.telegram.org/bot" + token
	hc := &http.Client{Timeout: 65 * time.Second}

	if err := tgSetMyCommands(ctx, hc, base); err != nil {
		fmt.Printf("[telegram-bot] setMyCommands: %v\n", err)
	}

	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := tgGetUpdates(ctx, hc, base, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Printf("[telegram-bot] getUpdates: %v\n", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			msg := u.Message
			if msg == nil {
				continue
			}
			if msg.Chat.ID != allowedChatID {
				// Only your TELEGRAM_CHAT_ID may control the bot; ignore others quietly.
				fmt.Printf("[telegram-bot] ignoring message from chat_id=%d\n", msg.Chat.ID)
				continue
			}
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}
			if !strings.HasPrefix(text, "/") {
				continue
			}
			cmd, _, _ := strings.Cut(text, " ")
			cmd = strings.ToLower(cmd)
			if i := strings.IndexByte(cmd, '@'); i >= 0 {
				cmd = cmd[:i]
			}

			switch cmd {
			case "/start", "/help":
				body := helpTelegramBody()
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID, body, msg.MessageID)
			case "/status":
				rep, err := buildScheduleStatusReport(ctx, cfg, client, locID, lookaheadDays)
				if err != nil {
					rep = "Error: " + err.Error()
				}
				out := "*Status*\n" + notify.EscapeMarkdownV2(rep)
				if err := tgSendMessage(ctx, hc, base, msg.Chat.ID, out, msg.MessageID); err != nil {
					fmt.Printf("[telegram-bot] send status: %v\n", err)
				}
			default:
				h := "*Unknown command*\n" + notify.EscapeMarkdownV2("Try /help or /status.")
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID, h, msg.MessageID)
			}
		}
	}
}

func helpTelegramBody() string {
	a := notify.EscapeMarkdownV2("I send booking-window alerts and daemon lifecycle messages here.")
	b := notify.EscapeMarkdownV2("/start and /help show this text. /status fetches your live schedule from Arbox (same logic as the CLI resolve command).")
	c := notify.EscapeMarkdownV2("Tip: in Telegram, tap the / button to open the command menu.")
	return "*Arbox scheduler*\n\n" + a + "\n\n" + b + "\n\n" + c
}

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message    *struct {
		MessageID int64 `json:"message_id"`
		Text      string
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func tgSetMyCommands(ctx context.Context, hc *http.Client, base string) error {
	payload := map[string]any{
		"commands": []map[string]string{
			{"command": "start", "description": "About this bot"},
			{"command": "help", "description": "List commands"},
			{"command": "status", "description": "Schedule + next booking windows"},
		},
	}
	return tgPostJSON(ctx, hc, base+"/setMyCommands", payload)
}

func tgGetUpdates(ctx context.Context, hc *http.Client, base string, offset int64) ([]tgUpdate, error) {
	u, err := url.Parse(base + "/getUpdates")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("timeout", "25")
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("getUpdates HTTP %d: %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if !parsed.OK {
		return nil, fmt.Errorf("getUpdates ok=false")
	}
	return parsed.Result, nil
}

func tgSendMessage(ctx context.Context, hc *http.Client, base string, chatID int64, text string, replyTo int64) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
	}
	if replyTo > 0 {
		payload["reply_to_message_id"] = replyTo
	}
	return tgPostJSON(ctx, hc, base+"/sendMessage", payload)
}

func tgPostJSON(ctx context.Context, hc *http.Client, urlStr string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram %s: %s", resp.Status, string(body))
	}
	var parsed struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &parsed)
	if !parsed.OK {
		return fmt.Errorf("telegram ok=false: %s", parsed.Description)
	}
	return nil
}
