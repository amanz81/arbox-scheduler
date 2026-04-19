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
	"unicode/utf8"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/notify"
)

// tgPlainChunkBytes is the max UTF-8 size for follow-up /status chunks sent
// without MarkdownV2 (Telegram limit is 4096; leave headroom).
const tgPlainChunkBytes = 4000

// tgFirstMarkdownBodyBytes caps the plain-text body in the first /status
// message so "*Status*\n" + EscapeMarkdownV2(body) stays under ~4096 after
// escaping (escaping can grow the string).
const tgFirstMarkdownBodyBytes = 2800

// runTelegramCommandBot registers slash commands with Telegram and long-polls
// getUpdates. cfgReload should return the merged config (config.yaml +
// user_plan.yaml overlay) so /status and /setup always see the latest file.
//
// Only TELEGRAM_CHAT_ID may control the bot.
func runTelegramCommandBot(ctx context.Context, token string, allowedChatID int64, cfgReload func() (*config.Config, error), client *arboxapi.Client, locID, lookaheadDays int) {
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

			if cq := u.CallbackQuery; cq != nil {
				if cq.Message == nil {
					_ = tgAnswerCallback(ctx, hc, base, cq.ID, "internal")
					continue
				}
				if cq.Message.Chat.ID != allowedChatID {
					continue
				}
				if err := handleSetupCallback(ctx, hc, base, cq); err != nil {
					fmt.Printf("[telegram-bot] callback: %v\n", err)
				}
				continue
			}

			msg := u.Message
			if msg == nil {
				continue
			}
			if msg.Chat.ID != allowedChatID {
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
			cmd, rest, _ := strings.Cut(text, " ")
			cmd = strings.ToLower(cmd)
			if i := strings.IndexByte(cmd, '@'); i >= 0 {
				cmd = cmd[:i]
			}
			args := strings.Fields(rest)

			cfg, err := cfgReload()
			if err != nil {
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
					"*Config error*\n"+notify.EscapeMarkdownV2(err.Error()), msg.MessageID)
				continue
			}

			switch cmd {
			case "/start", "/help":
				body := helpTelegramBody()
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID, body, msg.MessageID)
			case "/status":
				rep, err := buildStatusShortReport(ctx, cfg, client, locID, lookaheadDays)
				if err != nil {
					out := "*Status*\n" + notify.EscapeMarkdownV2("Error: "+err.Error())
					if sendErr := tgSendMessage(ctx, hc, base, msg.Chat.ID, out, msg.MessageID); sendErr != nil {
						fmt.Printf("[telegram-bot] send status error reply: %v\n", sendErr)
					}
					continue
				}
				if err := tgSendChunkedReport(ctx, hc, base, msg.Chat.ID, msg.MessageID, "Status", rep); err != nil {
					fmt.Printf("[telegram-bot] send status: %v\n", err)
				}
			case "/evening":
				startH, endH, days, parseErr := parseMorningArgs(args, 16, 22, 1)
				if parseErr != nil {
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
						"*Evening*\n"+notify.EscapeMarkdownV2(parseErr.Error()+"\nUsage: /evening [HH-HH] [days|week]"),
						msg.MessageID)
					continue
				}
				rep, err := buildClassWindowReport(ctx, cfg, client, locID, startH, endH, days)
				if err != nil {
					out := "*Evening*\n" + notify.EscapeMarkdownV2("Error: "+err.Error())
					if sendErr := tgSendMessage(ctx, hc, base, msg.Chat.ID, out, msg.MessageID); sendErr != nil {
						fmt.Printf("[telegram-bot] send evening error reply: %v\n", sendErr)
					}
					continue
				}
				if err := tgSendChunkedReport(ctx, hc, base, msg.Chat.ID, msg.MessageID, "Evening", rep); err != nil {
					fmt.Printf("[telegram-bot] send evening: %v\n", err)
				}
			case "/pause":
				loc := cfg.Location()
				now := time.Now().In(loc)
				until, reason, perr := parsePauseArgs(args, now, loc)
				if perr != nil {
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
						"*Pause*\n"+notify.EscapeMarkdownV2(perr.Error()), msg.MessageID)
					continue
				}
				if err := writePauseState(pauseState{PausedUntil: until, Reason: reason, UpdatedAt: now}); err != nil {
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
						"*Pause failed*\n"+notify.EscapeMarkdownV2(err.Error()), msg.MessageID)
					continue
				}
				body := fmt.Sprintf("Paused until %s (~%s).", until.Format("Mon 02 Jan 15:04 MST"), shortDuration(until.Sub(now).Round(time.Minute)))
				if reason != "" {
					body += " Reason: " + reason
				}
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
					"*Pause*\n"+notify.EscapeMarkdownV2(body), msg.MessageID)
			case "/resume":
				if err := clearPauseState(); err != nil {
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
						"*Resume failed*\n"+notify.EscapeMarkdownV2(err.Error()), msg.MessageID)
					continue
				}
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
					"*Resume*\n"+notify.EscapeMarkdownV2("Pause cleared. Auto-booking re-enabled."), msg.MessageID)
			case "/version":
				body := buildVersionReport(cfg, locID, lookaheadDays)
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
					"*Version*\n"+notify.EscapeMarkdownV2(body), msg.MessageID)
			case "/selftest":
				checks := runSelfTest(ctx, cfg, client, locID, lookaheadDays)
				body := formatSelfTestReport(checks)
				if next := nextPlannedBookingsSummary(cfg, lookaheadDays, 3); len(next) > 0 {
					body += "\nNext scheduled bookings:\n"
					for _, l := range next {
						body += "  · " + l + "\n"
					}
				}
				_ = tgSendChunkedReport(ctx, hc, base, msg.Chat.ID, msg.MessageID, "Self-test", body)
			case "/morning":
				startH, endH, days, parseErr := parseMorningArgs(args, 6, 12, 1)
				if parseErr != nil {
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID,
						"*Morning*\n"+notify.EscapeMarkdownV2(parseErr.Error()+"\nUsage: /morning [HH-HH] [days]"),
						msg.MessageID)
					continue
				}
				rep, err := buildMorningReport(ctx, cfg, client, locID, startH, endH, days)
				if err != nil {
					out := "*Morning*\n" + notify.EscapeMarkdownV2("Error: "+err.Error())
					if sendErr := tgSendMessage(ctx, hc, base, msg.Chat.ID, out, msg.MessageID); sendErr != nil {
						fmt.Printf("[telegram-bot] send morning error reply: %v\n", sendErr)
					}
					continue
				}
				if err := tgSendChunkedReport(ctx, hc, base, msg.Chat.ID, msg.MessageID, "Morning", rep); err != nil {
					fmt.Printf("[telegram-bot] send morning: %v\n", err)
				}
			case "/setup":
				if err := handleTelegramSetup(ctx, hc, base, msg.Chat.ID, msg.MessageID, cfg, client, locID); err != nil {
					e := "*Setup failed*\n" + notify.EscapeMarkdownV2(err.Error())
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID, e, msg.MessageID)
				}
			case "/setupdone":
				if err := handleSetupDone(ctx, hc, base, msg.Chat.ID, msg.MessageID); err != nil {
					fmt.Printf("[telegram-bot] setupdone: %v\n", err)
					fallback := "*Telegram send failed*\n" + notify.EscapeMarkdownV2(err.Error())
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID, fallback, msg.MessageID)
				}
			case "/setupcancel":
				if err := handleSetupCancel(ctx, hc, base, msg.Chat.ID, msg.MessageID); err != nil {
					fmt.Printf("[telegram-bot] setupcancel: %v\n", err)
					fallback := "*Telegram send failed*\n" + notify.EscapeMarkdownV2(err.Error())
					_ = tgSendMessage(ctx, hc, base, msg.Chat.ID, fallback, msg.MessageID)
				}
			default:
				h := "*Unknown command*\n" + notify.EscapeMarkdownV2(
					"Try /start, /help, /status, /morning, /evening, /setup, /setupdone, /setupcancel, /pause, /resume, /version, /selftest.")
				_ = tgSendMessage(ctx, hc, base, msg.Chat.ID, h, msg.MessageID)
			}
		}
	}
}

func helpTelegramBody() string {
	a := notify.EscapeMarkdownV2("I send booking-window alerts and daemon lifecycle messages here.")
	b := notify.EscapeMarkdownV2("/status — saved selections + your Arbox bookings. /morning [HH-HH] [days|week] — live classes (default 06-12, 1 day; e.g. /morning week). /evening [HH-HH] [days|week] — same for evenings (default 16-22). /setup + /setupdone save user_plan.yaml. /pause [Nh|Nd|until DATE] + /resume control auto-booking. /version shows deployed build + gym + TZ. /selftest runs health checks + lists next scheduled bookings (also included in the daily heartbeat).")
	c := notify.EscapeMarkdownV2("Tip: tap / in Telegram to open the command menu.")
	return "*Arbox scheduler*\n\n" + a + "\n\n" + b + "\n\n" + c
}

// tgCallbackQuery is Telegram's callback_query payload (inline keyboard tap).
type tgCallbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message *struct {
		MessageID int64 `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

type tgUpdate struct {
	UpdateID      int64              `json:"update_id"`
	Message       *tgIncomingMessage `json:"message"`
	CallbackQuery *tgCallbackQuery   `json:"callback_query"`
}

type tgIncomingMessage struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

func tgSetMyCommands(ctx context.Context, hc *http.Client, base string) error {
	payload := map[string]any{
		"commands": []map[string]string{
			{"command": "start", "description": "About this bot"},
			{"command": "help", "description": "List commands"},
			{"command": "status", "description": "Selections + your bookings"},
			{"command": "morning", "description": "Morning classes [HH-HH] [days|week]"},
			{"command": "evening", "description": "Evening classes [HH-HH] [days|week]"},
			{"command": "pause", "description": "Pause auto-booking [Nh|Nd|until DATE]"},
			{"command": "resume", "description": "Resume auto-booking"},
			{"command": "version", "description": "Show deployed version + gym + TZ"},
			{"command": "selftest", "description": "Health check + next scheduled bookings"},
			{"command": "setup", "description": "Pick week from real Arbox classes"},
			{"command": "setupdone", "description": "Save picks to user_plan.yaml"},
			{"command": "setupcancel", "description": "Abort /setup wizard"},
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
	return tgSendMessageMode(ctx, hc, base, chatID, text, replyTo, true)
}

// tgSendMessagePlain sends plain text (no parse_mode) — safe for long
// continuations where MarkdownV2 would be awkward to split.
func tgSendMessagePlain(ctx context.Context, hc *http.Client, base string, chatID int64, text string, replyTo int64) error {
	return tgSendMessageMode(ctx, hc, base, chatID, text, replyTo, false)
}

func tgSendMessageMode(ctx context.Context, hc *http.Client, base string, chatID int64, text string, replyTo int64, markdownV2 bool) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if markdownV2 {
		payload["parse_mode"] = "MarkdownV2"
	}
	if replyTo > 0 {
		payload["reply_to_message_id"] = replyTo
	}
	return tgPostJSON(ctx, hc, base+"/sendMessage", payload)
}

// tgSendChunkedReport sends a titled MarkdownV2 report, splitting the body
// across messages if needed (title is plain ASCII, no special chars).
func tgSendChunkedReport(ctx context.Context, hc *http.Client, base string, chatID, replyTo int64, title string, rep string) error {
	firstChunks := splitTelegramByteChunks(rep, tgFirstMarkdownBodyBytes)
	if len(firstChunks) == 0 {
		firstChunks = []string{""}
	}
	head := "*" + notify.EscapeMarkdownV2(title) + "*\n" + notify.EscapeMarkdownV2(firstChunks[0])
	if err := tgSendMessage(ctx, hc, base, chatID, head, replyTo); err != nil {
		return err
	}
	rest := joinByteChunks(firstChunks[1:])
	for len(rest) > 0 {
		take := tgPlainChunkBytes
		if len(rest) < take {
			take = len(rest)
		} else {
			// Prefer a newline break in the tail of this window.
			if cut := strings.LastIndexByte(rest[:take], '\n'); cut >= take*2/3 && cut > 0 {
				take = cut + 1
			} else {
				for take > 0 && !utf8.RuneStart(rest[take]) {
					take--
				}
				if take == 0 {
					_, sz := utf8.DecodeRuneInString(rest)
					take = sz
				}
			}
		}
		chunk := rest[:take]
		rest = rest[take:]
		if err := tgSendMessagePlain(ctx, hc, base, chatID, chunk, 0); err != nil {
			return err
		}
	}
	return nil
}

func joinByteChunks(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts, "")
}

// splitTelegramByteChunks splits s into non-empty UTF-8-safe pieces of at most
// maxBytes each, preferring boundaries at newlines.
func splitTelegramByteChunks(s string, maxBytes int) []string {
	if maxBytes < 64 {
		maxBytes = 64
	}
	if len(s) <= maxBytes {
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		if len(s) <= maxBytes {
			out = append(out, s)
			break
		}
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			_, sz := utf8.DecodeRuneInString(s)
			cut = sz
		}
		if nl := strings.LastIndexByte(s[:cut], '\n'); nl >= cut*2/3 && nl > 0 {
			cut = nl + 1
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

func tgEditMessageReplyMarkup(ctx context.Context, hc *http.Client, base string, chatID, messageID int64, keyboard [][]map[string]string) error {
	payload := map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": map[string]any{"inline_keyboard": keyboard},
	}
	return tgPostJSON(ctx, hc, base+"/editMessageReplyMarkup", payload)
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
