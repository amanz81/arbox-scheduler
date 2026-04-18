package notify

import "strings"

// EscapeMarkdownV2 escapes arbitrary plain text for Telegram MarkdownV2
// (outside of explicit *bold* / `code` / etc. wrappers you build yourself).
//
// Per https://core.telegram.org/bots/api#markdownv2-style — characters
// _ * [ ] ( ) ~ ` > # + - = | { } . ! and backslash must be escaped.
func EscapeMarkdownV2(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!':
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeMarkdownV2Code escapes text that will appear *inside* a pair of
// backticks for inline monospace in MarkdownV2. Only ` and \ need escaping
// there, but escaping the full set keeps callers from accidentally breaking
// out of the code span.
func escapeMarkdownV2Code(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\', '`':
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
