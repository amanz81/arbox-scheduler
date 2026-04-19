package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestTelegramCommandsConsistent asserts that telegramCommandList (registered
// with Telegram via setMyCommands) and the dispatcher switch in
// runTelegramCommandBot are kept in lockstep. A future edit that adds a
// command to one without the other will fail this test.
func TestTelegramCommandsConsistent(t *testing.T) {
	src, err := os.ReadFile("telegram_bot.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// Extract every "/<name>" mentioned in a `case "/foo", "/bar":` clause in
	// the dispatcher switch. Multiple commands on one case (e.g. /start,/help)
	// are handled by re-running the regex over the whole body.
	caseRe := regexp.MustCompile(`case ((?:"/[a-zA-Z]+",?\s*)+):`)
	tokenRe := regexp.MustCompile(`"/([a-zA-Z]+)"`)
	switchCmds := map[string]bool{}
	for _, m := range caseRe.FindAllStringSubmatch(body, -1) {
		for _, tok := range tokenRe.FindAllStringSubmatch(m[1], -1) {
			switchCmds[tok[1]] = true
		}
	}
	if len(switchCmds) == 0 {
		t.Fatal("found no `case \"/X\"` in telegram_bot.go — regex broken?")
	}

	registered := map[string]bool{}
	for _, c := range telegramCommandList {
		registered[c.Name] = true
	}

	// Symmetric difference.
	var missingHandler, missingRegistered []string
	for cmd := range registered {
		if !switchCmds[cmd] {
			missingHandler = append(missingHandler, cmd)
		}
	}
	for cmd := range switchCmds {
		if !registered[cmd] {
			missingRegistered = append(missingRegistered, cmd)
		}
	}
	sort.Strings(missingHandler)
	sort.Strings(missingRegistered)

	if len(missingHandler) > 0 {
		t.Errorf("commands registered with Telegram but no switch case: %s",
			strings.Join(missingHandler, ", "))
	}
	if len(missingRegistered) > 0 {
		t.Errorf("commands handled in switch but not in telegramCommandList: %s",
			strings.Join(missingRegistered, ", "))
	}
}

func TestTelegramCommandList_NoEmptyOrDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range telegramCommandList {
		if c.Name == "" {
			t.Errorf("empty Name in telegramCommandList")
		}
		if c.Description == "" {
			t.Errorf("%s has empty Description", c.Name)
		}
		if seen[c.Name] {
			t.Errorf("duplicate command: %s", c.Name)
		}
		seen[c.Name] = true
		if strings.HasPrefix(c.Name, "/") {
			t.Errorf("%s should be stored without leading slash", c.Name)
		}
	}
}
