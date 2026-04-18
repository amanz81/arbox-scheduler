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
func handleTelegramSetup(ctx context.Context, hc *http.Client, base string, chatID, replyTo int64, cfg *config.Config, client *arboxapi.Client, locID int) error {
	cands, err := buildSetupCandidates(ctx, cfg, client, locID, setupHorizonDays)
	if err != nil {
		return err
	}
	sess := &setupSession{
		Version:    1,
		Candidates: cands,
		Picks:      map[string][]int{},
	}
	if err := writeSetupSession(sess); err != nil {
		return err
	}

	head := "*Weekly setup*\n" + notify.EscapeMarkdownV2(
		"Classes below are read live from Arbox for the next upcoming occurrence of each weekday. Tap buttons to toggle (added/removed). First tapped = highest priority. Then send /setupdone to save to user_plan.yaml, or /setupcancel to abort.")

	_ = tgSendMessage(ctx, hc, base, chatID, head, replyTo)

	for _, dayKey := range setupWeekdayOrder {
		row, ok := cands[dayKey]
		if !ok || len(row) == 0 {
			continue
		}
		var kb [][]map[string]string
		var cur []map[string]string
		for i, c := range row {
			cb := fmt.Sprintf("s|%s|%d", dayKey, i)
			if len(cb) > 64 {
				return fmt.Errorf("callback_data too long for %s idx %d", dayKey, i)
			}
			btn := map[string]string{
				"text":          truncateRunes(c.Label, 64),
				"callback_data": cb,
			}
			cur = append(cur, btn)
			if len(cur) >= 4 {
				kb = append(kb, cur)
				cur = nil
			}
		}
		if len(cur) > 0 {
			kb = append(kb, cur)
		}
		title := "*" + notify.EscapeMarkdownV2(prettyDayKey(dayKey)) + "*\n" +
			notify.EscapeMarkdownV2("Tap to include/exclude this slot.")
		if err := tgSendMessageWithKeyboard(ctx, hc, base, chatID, title, 0, kb); err != nil {
			return err
		}
	}
	foot := "*Next steps*\n" + notify.EscapeMarkdownV2("When finished: /setupdone to save (overrides those weekdays in user_plan.yaml) or /setupcancel.")
	return tgSendMessage(ctx, hc, base, chatID, foot, 0)
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
	return tgSendMessage(ctx, hc, base, chatID, ok, replyTo)
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
