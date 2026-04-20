// arbox-mcp is a minimal MCP (Model Context Protocol) stdio shim that
// lets nanobot (or any MCP-aware LLM client) use arbox-scheduler as a
// native MCP tool server.
//
// Why a shim at all:
//
//   The main arbox daemon already serves a REST API at /api/v1/... with
//   an OpenAPI 3.1 spec (see cmd/arbox/http_api.go + docs/API.md). That
//   surface works directly with OpenAI function-calling, Claude Code
//   with a GenericHTTP tool, Cursor agent mode, and anything else that
//   understands OpenAPI. But nanobot's mcpServers config only accepts
//   native MCP transports (stdio / sse / streamableHttp speaking
//   JSON-RPC 2.0 — see nanobot.config.schema.MCPServerConfig). So we
//   need a thin translator: MCP stdio in, HTTP REST out.
//
// This shim stays deliberately simple:
//
//   - Tools are hardcoded (see toolDefs). Small surface, and keeping it
//     static means an LLM with stale cached tool descriptions stays
//     correct. To add a tool: add an entry here AND land the REST route
//     in cmd/arbox.
//   - stdio framing is newline-delimited JSON (json.Encoder + Scanner).
//     That matches the MCP stdio transport implemented by nanobot,
//     Claude Desktop, and other clients.
//   - No concurrency inside the shim — each request handled inline.
//     We still forward through the HTTP API which does its own
//     bookerMu + audit for mutations, so we inherit the same safety
//     properties as the /api/v1 callers.
//
// Environment:
//
//   ARBOX_API_URL    — default "http://127.0.0.1:8080" (loopback).
//   ARBOX_API_TOKEN  — required. Read or admin token; read is safer.
//
// Protocol reference:
//
//   MCP 2025-06-18. Methods handled: initialize, initialized (notif),
//   tools/list, tools/call, ping, shutdown. Everything else returns
//   method-not-found so unknown traffic is visible rather than silent.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Build-time ldflag: -X main.Version=<short-sha>. Defaults to "dev" for
// local builds. The shim advertises this in MCP initialize so clients
// can log which translator they're talking to.
var Version = "dev"

const (
	mcpProtocolVersion = "2025-06-18"
	serverName         = "arbox-mcp"
	httpTimeout        = 30 * time.Second
)

func main() {
	apiURL := strings.TrimRight(strings.TrimSpace(os.Getenv("ARBOX_API_URL")), "/")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:8080"
	}
	token := strings.TrimSpace(os.Getenv("ARBOX_API_TOKEN"))
	if token == "" {
		fatalf("ARBOX_API_TOKEN is required (read or admin token from ~/arbox/data/.env)")
	}

	s := &server{
		apiURL: apiURL,
		token:  token,
		http:   &http.Client{Timeout: httpTimeout},
		out:    json.NewEncoder(os.Stdout),
	}
	if err := s.run(os.Stdin); err != nil && err != io.EOF {
		fatalf("fatal: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[arbox-mcp] "+format+"\n", args...)
	os.Exit(1)
}

// server is the stdio MCP handler. One per process; not safe for
// concurrent use (MCP stdio is strictly request/response serialised by
// the framing layer).
type server struct {
	apiURL string
	token  string
	http   *http.Client
	out    *json.Encoder
}

// run reads newline-delimited JSON-RPC 2.0 messages from r until EOF
// and dispatches each one. Bufio scanner limits help us not blow up on
// a malformed client, but we lift the token size so tools/call with a
// realistic OpenAPI args payload fits.
func (s *server) run(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env envelope
		if err := json.Unmarshal(line, &env); err != nil {
			s.respondError(nil, -32700, "parse error: "+err.Error())
			continue
		}
		s.dispatch(env)
	}
	return sc.Err()
}

// envelope is the shape of both JSON-RPC requests and notifications.
// `id` is absent on notifications; we distinguish by its rawness.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *server) dispatch(env envelope) {
	switch env.Method {
	case "initialize":
		s.respondResult(env.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    serverName,
				"version": Version,
			},
		})
	case "initialized", "notifications/initialized":
		// Notification; no response per spec.
	case "ping":
		s.respondResult(env.ID, map[string]any{})
	case "tools/list":
		s.respondResult(env.ID, map[string]any{
			"tools": mcpTools(),
		})
	case "tools/call":
		s.handleToolsCall(env)
	case "shutdown":
		s.respondResult(env.ID, nil)
		os.Exit(0)
	default:
		// Notifications (no id) get no response. Requests get method-not-found.
		if len(env.ID) == 0 {
			return
		}
		s.respondError(env.ID, -32601, "method not found: "+env.Method)
	}
}

// handleToolsCall maps an MCP tool name to an HTTP REST call, forwards
// the named arguments as either query string (GET) or JSON body (POST),
// and wraps the response as an MCP tool-call result.
func (s *server) handleToolsCall(env envelope) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(env.Params, &p); err != nil {
		s.respondError(env.ID, -32602, "invalid params: "+err.Error())
		return
	}
	def, ok := toolIndex[p.Name]
	if !ok {
		s.respondError(env.ID, -32602, "unknown tool: "+p.Name)
		return
	}

	resp, status, err := s.call(def.method, def.path, p.Arguments)
	if err != nil {
		s.respondResult(env.ID, toolError(fmt.Sprintf("arbox request failed: %v", err)))
		return
	}

	// Non-2xx from upstream = a tool-level error (not a protocol error),
	// so return it as `isError: true` inside the tool result. That way
	// the LLM sees the message and can fix its next call instead of the
	// whole MCP session dying.
	if status >= 400 {
		s.respondResult(env.ID, toolError(fmt.Sprintf("HTTP %d: %s", status, string(resp))))
		return
	}

	s.respondResult(env.ID, map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": string(resp)},
		},
		"isError": false,
	})
}

// call issues the HTTP request to the upstream arbox REST API. For GET
// routes named args are rendered as query params; for POST routes the
// `confirm` key (if present) moves to ?confirm=1 and everything else
// goes in the JSON body — matches what the REST handlers expect.
func (s *server) call(method, path string, args map[string]any) ([]byte, int, error) {
	u, err := url.Parse(s.apiURL + path)
	if err != nil {
		return nil, 0, err
	}
	var body io.Reader

	if method == "GET" {
		q := u.Query()
		for k, v := range args {
			q.Set(k, fmt.Sprintf("%v", v))
		}
		u.RawQuery = q.Encode()
	} else {
		jsonBody := map[string]any{}
		q := u.Query()
		for k, v := range args {
			if k == "confirm" {
				// Preserve dry-run-by-default: only set ?confirm=1 when
				// the caller explicitly passes confirm: true.
				if b, ok := v.(bool); ok && b {
					q.Set("confirm", "1")
				}
				continue
			}
			jsonBody[k] = v
		}
		u.RawQuery = q.Encode()
		b, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := s.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, 0, err
	}
	return buf, res.StatusCode, nil
}

// toolError wraps an MCP tool result in the "text error" shape most
// LLM clients expect when isError=true.
func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": msg},
		},
		"isError": true,
	}
}

// respondResult writes a successful JSON-RPC 2.0 response to stdout.
// No-op when id is empty (notifications must not get a response).
func (s *server) respondResult(id json.RawMessage, result any) {
	if len(id) == 0 {
		return
	}
	_ = s.out.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, result})
}

func (s *server) respondError(id json.RawMessage, code int, msg string) {
	// Guarantee a valid id shape. MCP lets us reply with null id on
	// parse errors when we can't recover the original.
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	_ = s.out.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   map[string]any  `json:"error"`
	}{"2.0", id, map[string]any{
		"code":    code,
		"message": msg,
	}})
}
