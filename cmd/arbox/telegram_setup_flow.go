package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/notify"
)

const setupHorizonDays = 14

func tgAnswerCallback(ctx context.Context, hc *http.Client, base, callbackID, text string) error {
	return tgPostJSON(ctx, hc, base+"/answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        false,
	})
}

func tgSendMessageWithKeyboard(ctx context.Context, hc *http.Client, base string, chatID int64, text string, replyTo int64, keyboard [][]map[string]string) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
		"reply_markup": map[string]any{
			"inline_keyboard": keyboard,
		},
	}
	if replyTo > 0 {
		payload["reply_to_message_id"] = replyTo
	}
	return tgPostJSON(ctx, hc, base+"/sendMessage", payload)
}

// handleTelegramSetup fetches real Arbox classes and sends one inline-keyboard
// message per weekday that has at least one candidate.
func totalCandidates(cands map[string][]setupCandidate) int {
	n := 0
	for _, row := range cands {
		n += len(row)
	}
	return n
}

func handleTelegramSetup(ctx context.Context, hc *http.Client, base string, chatID, replyTo int64, cfg *config.Config, client *arboxapi.Client, locID int) error {
	cands, err := buildSetupCandidates(ctx, cfg, client, locID, setupHorizonDays)
	if err != nil {
		return err
	}
	if totalCandidates(cands) == 0 {
		_ = deleteSetupSession()
		msg := "*Weekly setup*\n" + notify.EscapeMarkdownV2(
			"No classes matched your filters for the next two weeks of real Arbox schedules (per weekday). "+
				"Check category_filter in config and that the box has published classes, then try /setup again.")
		return tgSendMessage(ctx, hc, base, chatID, msg, replyTo)
	}
	sess := &setupSession{
		Version:    1,
		Candidates: cands,
		Picks:      seedSetupPicksFromConfig(cfg, cands),
	}
	if err := writeSetupSession(sess); err != nil {
		return err
	}

	head := "*Weekly setup*\n" + notify.EscapeMarkdownV2(
		"Each message below is one weekday. Buttons are real classes from Arbox (next occurrence of that weekday). "+
			"✓ = selected for your plan, ○ = not selected (buttons update when you tap). "+
			"When you open /setup, ✓ marks are filled from your current saved plan when times/categories match. "+
			"Tap to toggle. First tap = highest priority for that day. "+
			"Send /setupdone to write user_plan.yaml on the server, or /setupcancel to discard this session.")

	_ = tgSendMessage(ctx, hc, base, chatID, head, replyTo)

	for _, dayKey := range setupWeekdayOrder {
		row, ok := cands[dayKey]
		if !ok || len(row) == 0 {
			continue
		}
		kb, kbErr := buildSetupInlineKeyboard(dayKey, row, sess.Picks[dayKey])
		if kbErr != nil {
			return kbErr
		}
		title := "*" + notify.EscapeMarkdownV2(prettyDayKey(dayKey)) + "*\n" +
			notify.EscapeMarkdownV2("✓ on = daemon may book this slot. ○ off = ignored. Saving with every slot ○ on a day makes that weekday a rest day (no auto-booking).")
		if err := tgSendMessageWithKeyboard(ctx, hc, base, chatID, title, 0, kb); err != nil {
			return err
		}
	}
	foot := "*Next steps*\n" + notify.EscapeMarkdownV2(
		"/setupdone — save checked slots to user_plan.yaml (only the weekdays shown above are updated). "+
			"/setupcancel — throw away this session without changing the file.")
	return tgSendMessage(ctx, hc, base, chatID, foot, 0)
}

// setupButtonsPerRow controls keyboard width. Narrow phones truncate text
// when too many buttons share a row; 2 keeps labels fully readable.
const setupButtonsPerRow = 2

// buildSetupInlineKeyboard returns Telegram inline_keyboard rows; button text
// is capped at 64 chars. prefix shows selection state (✓ / ○).
func buildSetupInlineKeyboard(dayKey string, row []setupCandidate, picks []int) ([][]map[string]string, error) {
	inPick := make(map[int]bool, len(picks))
	for _, idx := range picks {
		inPick[idx] = true
	}
	var kb [][]map[string]string
	var cur []map[string]string
	for i, c := range row {
		prefix := "○ "
		if inPick[i] {
			prefix = "✓ "
		}
		cb := fmt.Sprintf("s|%s|%d", dayKey, i)
		if len(cb) > 64 {
			return nil, fmt.Errorf("callback_data too long for %s idx %d", dayKey, i)
		}
		btn := map[string]string{
			"text":          truncateRunes(prefix+c.Label, 64),
			"callback_data": cb,
		}
		cur = append(cur, btn)
		if len(cur) >= setupButtonsPerRow {
			kb = append(kb, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		kb = append(kb, cur)
	}
	return kb, nil
}

func handleSetupCallback(ctx context.Context, hc *http.Client, base string, cq *tgCallbackQuery) error {
	data := strings.TrimSpace(cq.Data)
	if !strings.HasPrefix(data, "s|") {
		return tgAnswerCallback(ctx, hc, base, cq.ID, "ignored")
	}
	parts := strings.Split(data, "|")
	if len(parts) != 3 {
		return tgAnswerCallback(ctx, hc, base, cq.ID, "bad payload")
	}
	dayKey := strings.ToLower(parts[1])
	idx, err := strconv.Atoi(parts[2])
	if err != nil {
		return tgAnswerCallback(ctx, hc, base, cq.ID, "bad index")
	}
	s, err := readSetupSession()
	if err != nil || s == nil {
		return tgAnswerCallback(ctx, hc, base, cq.ID, "no session; run /setup")
	}
	cands := s.Candidates[dayKey]
	if idx < 0 || idx >= len(cands) {
		return tgAnswerCallback(ctx, hc, base, cq.ID, "stale button; /setup again")
	}
	action := togglePick(s, dayKey, idx)
	if err := writeSetupSession(s); err != nil {
		return tgAnswerCallback(ctx, hc, base, cq.ID, "save failed")
	}
	if cq.Message != nil {
		row := s.Candidates[dayKey]
		kb, err := buildSetupInlineKeyboard(dayKey, row, s.Picks[dayKey])
		if err == nil {
			if err := tgEditMessageReplyMarkup(ctx, hc, base, cq.Message.Chat.ID, cq.Message.MessageID, kb); err != nil {
				fmt.Printf("[telegram-bot] editMessageReplyMarkup: %v\n", err)
			}
		}
	}
	return tgAnswerCallback(ctx, hc, base, cq.ID, action)
}

func handleSetupDone(ctx context.Context, hc *http.Client, base string, chatID, replyTo int64) error {
	s, err := readSetupSession()
	if err != nil || s == nil {
		return tgSendMessage(ctx, hc, base, chatID,
			"*Nothing to save*\n"+notify.EscapeMarkdownV2("Run /setup first."), replyTo)
	}
	if err := writeUserPlanFromSession(cfgPath, s); err != nil {
		msg := "*Save failed*\n" + notify.EscapeMarkdownV2(err.Error())
		return tgSendMessage(ctx, hc, base, chatID, msg, replyTo)
	}
	ok := "*Saved*\n" + notify.EscapeMarkdownV2("Wrote "+userPlanOverlayPath()+". The daemon reloads this file every tick; no restart required.")
	if err := tgSendMessage(ctx, hc, base, chatID, ok, replyTo); err != nil {
		// YAML is already written and session cleared; plain text avoids losing
		// confirmation if MarkdownV2 trips on an unexpected character.
		plain := "Saved to " + userPlanOverlayPath() + ". (Telegram formatted message failed: " + err.Error() + ")"
		return tgSendMessagePlain(ctx, hc, base, chatID, plain, replyTo)
	}
	return nil
}

func handleSetupCancel(ctx context.Context, hc *http.Client, base string, chatID, replyTo int64) error {
	_ = deleteSetupSession()
	return tgSendMessage(ctx, hc, base, chatID,
		"*Cancelled*\n"+notify.EscapeMarkdownV2("Setup session cleared."), replyTo)
}

func prettyDayKey(dayKey string) string {
	if dayKey == "" {
		return dayKey
	}
	return strings.ToUpper(dayKey[:1]) + dayKey[1:]
}
