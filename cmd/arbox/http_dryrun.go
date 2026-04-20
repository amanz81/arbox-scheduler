package main

// Helpers for the dry-run / confirm policy on mutation endpoints.
//
// Every mutation endpoint requires the caller to opt in to the real upstream
// call by passing ?confirm=1. Without confirm we still parse and validate
// the request, write an audit log line tagged confirm:false, and return a
// 200 with `actual_send: false` describing what *would* have been sent.
//
// This belt-and-suspenders default makes accidental destructive calls from
// agents (Claude / nanobot / OpenAI) much harder.

import (
	"net/http"
)

// hasConfirm reports whether the request URL contains ?confirm=1 (the only
// accepted truthy value, to avoid surprises with "true" / "yes" etc.).
func hasConfirm(r *http.Request) bool {
	return r.URL.Query().Get("confirm") == "1"
}

// dryRunResponse is the JSON shape returned for any mutation called without
// ?confirm=1. `would_send` is the parsed request body so the caller can
// echo back what they intended.
type dryRunResponse struct {
	WouldSend  any    `json:"would_send"`
	ActualSend bool   `json:"actual_send"`
	Reason     string `json:"reason"`
	Route      string `json:"route"`
}

func writeDryRun(w http.ResponseWriter, route string, args any) {
	writeJSON(w, http.StatusOK, dryRunResponse{
		WouldSend:  args,
		ActualSend: false,
		Reason:     "add ?confirm=1 to the URL to actually send this request to Arbox",
		Route:      route,
	})
}
