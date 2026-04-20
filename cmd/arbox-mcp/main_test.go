package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// makeServer stands up a fake upstream arbox REST server and a shim
// wired to it, so every test can exchange real MCP JSON-RPC messages
// without touching a real daemon.
func makeServer(t *testing.T, handler http.HandlerFunc) (*server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(handler)
	t.Cleanup(up.Close)
	return &server{
		apiURL: up.URL,
		token:  "test-token",
		http:   up.Client(),
		// out is replaced per-test so assertions can inspect it.
	}, up
}

// runOne sends one JSON-RPC message through s.run and returns every
// stdout line it produced. Notifications (no id) yield no output, so
// callers for those cases should expect an empty slice.
func runOne(t *testing.T, s *server, msg string) []string {
	t.Helper()
	var out bytes.Buffer
	s.out = json.NewEncoder(&out)
	if err := s.run(strings.NewReader(msg + "\n")); err != nil && err != io.EOF {
		t.Fatalf("run: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func TestInitialize_ReturnsProtocolAndServerInfo(t *testing.T) {
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("initialize must not hit upstream; got %s %s", r.Method, r.URL.Path)
	})
	lines := runOne(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(lines) != 1 {
		t.Fatalf("expected 1 response line, got %d: %v", len(lines), lines)
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, lines[0])
	}
	if resp.Result.ProtocolVersion == "" {
		t.Errorf("protocolVersion empty")
	}
	if resp.Result.ServerInfo.Name != "arbox-mcp" {
		t.Errorf("wrong serverInfo.name: %q", resp.Result.ServerInfo.Name)
	}
	if _, ok := resp.Result.Capabilities["tools"]; !ok {
		t.Errorf("must declare tools capability")
	}
}

func TestInitializedNotification_NoResponse(t *testing.T) {
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {})
	// No id → notification → no response, per JSON-RPC 2.0.
	lines := runOne(t, s, `{"jsonrpc":"2.0","method":"initialized"}`)
	if len(lines) != 0 {
		t.Errorf("notification must produce no response, got: %v", lines)
	}
}

func TestToolsList_ContainsCoreReadAndMutations(t *testing.T) {
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("tools/list must not hit upstream")
	})
	lines := runOne(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantAny := map[string]bool{
		"arbox_version": false, "arbox_status": false,
		"arbox_book": false, "arbox_pause": false,
	}
	for _, tl := range resp.Result.Tools {
		if _, ok := wantAny[tl.Name]; ok {
			wantAny[tl.Name] = true
		}
	}
	for name, seen := range wantAny {
		if !seen {
			t.Errorf("missing expected tool %q in tools/list", name)
		}
	}
}

func TestToolsCall_ForwardsGETWithBearerAndQuery(t *testing.T) {
	var gotAuth, gotURL, gotMethod string
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotURL = r.URL.String()
		gotMethod = r.Method
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"ok":true,"echo":"hello"}`)
	})

	lines := runOne(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"arbox_status","arguments":{"days":3}}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 response, got %d", len(lines))
	}
	if gotMethod != "GET" {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("auth header = %q, want 'Bearer test-token'", gotAuth)
	}
	if !strings.Contains(gotURL, "/api/v1/status") {
		t.Errorf("path = %q, want /api/v1/status", gotURL)
	}
	if !strings.Contains(gotURL, "days=3") {
		t.Errorf("query = %q, want days=3", gotURL)
	}

	// isError: false, body echoed in content[0].text.
	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, lines[0])
	}
	if resp.Result.IsError {
		t.Errorf("isError should be false on 200 upstream")
	}
	if !strings.Contains(resp.Result.Content[0].Text, `"ok":true`) {
		t.Errorf("upstream body not echoed to content: %q", resp.Result.Content[0].Text)
	}
}

func TestToolsCall_MutationWithoutConfirm_NoQueryConfirm(t *testing.T) {
	var gotURL string
	var gotBody []byte
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"actual_send":false,"preview":"dry-run"}`)
	})

	_ = runOne(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"arbox_book","arguments":{"schedule_id":1234}}}`)

	// confirm: false → never ?confirm=1 on the URL.
	if strings.Contains(gotURL, "confirm=1") {
		t.Errorf("unexpected confirm=1 when arg was not set: %q", gotURL)
	}
	// Body must carry schedule_id but NOT confirm (that key gets stripped).
	var body map[string]any
	_ = json.Unmarshal(gotBody, &body)
	if body["schedule_id"] == nil {
		t.Errorf("schedule_id missing in body: %s", gotBody)
	}
	if _, hasConfirm := body["confirm"]; hasConfirm {
		t.Errorf("confirm leaked into JSON body; it must only move to ?confirm=1: %s", gotBody)
	}
}

func TestToolsCall_MutationWithConfirmTrue_AddsConfirmQuery(t *testing.T) {
	var gotURL string
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"actual_send":true}`)
	})

	_ = runOne(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"arbox_book","arguments":{"schedule_id":1234,"confirm":true}}}`)

	if !strings.Contains(gotURL, "confirm=1") {
		t.Errorf("confirm=true must add ?confirm=1; url=%q", gotURL)
	}
}

func TestToolsCall_UpstreamError_ReportsAsToolError(t *testing.T) {
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":"admin token required"}`)
	})
	lines := runOne(t, s, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"arbox_book","arguments":{"schedule_id":1}}}`)
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Result.IsError {
		t.Errorf("403 upstream must surface as tool-level isError=true")
	}
	if !strings.Contains(resp.Result.Content[0].Text, "403") {
		t.Errorf("error text should mention status; got %q", resp.Result.Content[0].Text)
	}
}

func TestToolsCall_UnknownTool_ReturnsProtocolError(t *testing.T) {
	s, _ := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unknown-tool must not hit upstream")
	})
	lines := runOne(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	var resp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error.Code != -32602 {
		t.Errorf("expected -32602 invalid params, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "unknown tool") {
		t.Errorf("error message should name the problem: %q", resp.Error.Message)
	}
}

func TestUnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	s, _ := makeServer(t, nil)
	lines := runOne(t, s, `{"jsonrpc":"2.0","id":8,"method":"resources/list"}`)
	var resp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected -32601 method-not-found, got %d", resp.Error.Code)
	}
}
