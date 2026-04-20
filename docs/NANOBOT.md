# Wiring arbox-scheduler into nanobot

There are two integration paths, depending on your LLM client:

1. **Path A — MCP stdio shim (this is what nanobot uses).** A small Go
   binary `arbox-mcp` speaks native MCP JSON-RPC on stdio, translates
   each `tools/call` into an HTTP call against the daemon's REST API,
   and ships the result back to nanobot. **Recommended for nanobot.**

2. **Path B — direct REST + OpenAPI.** Clients that can consume OpenAPI
   (Claude Code's custom HTTP tool, Cursor agent mode with an HTTP tool
   adapter, OpenAI Responses / function-calling with a small wrapper,
   `curl` scripts) hit `/api/v1/...` directly — see
   [API.md](API.md). **No shim, no extra process.** Pick this if you're
   not using nanobot.

Why both: nanobot's `mcpServers` config only accepts native MCP
transports (stdio / sse / streamableHttp over JSON-RPC 2.0 — see
`nanobot.config.schema.MCPServerConfig`). It doesn't read OpenAPI specs
directly. The shim is ~350 lines of Go and keeps the daemon's REST
surface as the single source of truth.

---

## Production setup on the same Oracle VM

Both the daemon and nanobot run on the same Oracle Free Tier VM, as the
same `ubuntu` user, under systemd. No network path involved in the
nanobot → arbox link: `arbox-mcp` is a subprocess nanobot launches over
stdio, and `arbox-mcp` itself calls the daemon at `http://127.0.0.1:8080`
(loopback). No ports exposed anywhere.

```
┌─ Oracle VM (ubuntu) ──────────────────────────────────────────────┐
│                                                                   │
│   arbox.service     ── daemon + /api/v1 on 127.0.0.1:8080         │
│                                    ▲                              │
│                                    │ HTTP Bearer (loopback)       │
│                                    │                              │
│   nanobot.service                  │                              │
│    └─ spawns ──► arbox-mcp ────────┘                              │
│                  (stdio JSON-RPC,                                 │
│                   MCP protocol 2025-06-18)                        │
└───────────────────────────────────────────────────────────────────┘
```

---

## 1. Enable the HTTP API on the daemon side

```bash
ssh oracle-vps 'cat >> ~/arbox/data/.env' <<EOF
ARBOX_API_READ_TOKEN=$(openssl rand -hex 32)
ARBOX_API_ADMIN_TOKEN=$(openssl rand -hex 32)
EOF
ssh oracle-vps 'sudo systemctl restart arbox'
ssh oracle-vps 'sudo journalctl -u arbox -n 5 --no-pager | grep "\[http\]"'
# → [http] listening on 127.0.0.1:8080 (read=… admin=…)
```

If you want nanobot to be able to book/cancel/pause, use the **admin**
token in step 3. Otherwise use the **read** token — safer default, the
LLM can still see everything but can't act.

Verify from the VM:

```bash
ssh oracle-vps 'curl -s http://127.0.0.1:8080/api/v1/healthz'
# → {"ok":true}
```

---

## 2. Drop the `arbox-mcp` binary on the VM

It ships next to the daemon binary under `~/arbox/bin/`. The repo's
`scripts/deploy-oracle.sh` cross-compiles both binaries on every run —
just re-run it after pulling this repo. Manual one-off:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath -ldflags "-s -w -X main.Version=$(git rev-parse --short HEAD)" \
  -o /tmp/arbox-mcp ./cmd/arbox-mcp
scp /tmp/arbox-mcp oracle-vps:~/arbox/bin/arbox-mcp
ssh oracle-vps 'chmod +x ~/arbox/bin/arbox-mcp'
```

Smoke-test the shim directly (no nanobot involved):

```bash
ssh oracle-vps 'set -a; . ~/arbox/data/.env; set +a; \
  ARBOX_API_TOKEN=$ARBOX_API_READ_TOKEN \
  printf "%s\n" "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}" \
  | ~/arbox/bin/arbox-mcp'
# → {"jsonrpc":"2.0","id":1,"result":{"capabilities":{…},"serverInfo":{"name":"arbox-mcp",…}}}
```

---

## 3. Wire nanobot

### 3a. Set the token

```bash
ssh oracle-vps 'cat >> ~/.nanobot/secrets.env' <<'EOF'

# Arbox scheduler MCP — read-only by default. Swap to ARBOX_API_ADMIN_TOKEN
# if you want nanobot to book / cancel / pause (mutations still require
# ?confirm=1 via the tool call, which arbox-mcp passes through).
ARBOX_API_TOKEN=<paste the ARBOX_API_READ_TOKEN value from ~/arbox/data/.env>
EOF
```

### 3b. Add the MCP server entry

Edit `~/.nanobot/config.json` and add under `tools.mcpServers`:

```json
"arbox": {
  "type": "stdio",
  "command": "/home/ubuntu/arbox/bin/arbox-mcp",
  "args": [],
  "env": {
    "ARBOX_API_URL":   "http://127.0.0.1:8080",
    "ARBOX_API_TOKEN": "${ARBOX_API_TOKEN}"
  },
  "tool_timeout": 30,
  "enabled_tools": [
    "arbox_version", "arbox_status", "arbox_classes",
    "arbox_morning", "arbox_evening", "arbox_bookings",
    "arbox_plan", "arbox_selftest",
    "arbox_book", "arbox_cancel", "arbox_pause", "arbox_resume"
  ]
}
```

**`enabled_tools` is required** — nanobot skips every tool not listed
there (treats `[]` as "none enabled"). Drop any tool name you don't want
the LLM to see.

### 3c. Restart nanobot and check the log

```bash
ssh oracle-vps 'sudo systemctl restart nanobot'
sleep 3
ssh oracle-vps 'sudo journalctl -u nanobot --since "10 seconds ago" \
  --no-pager | grep -E "arbox|tools registered"'
# → MCP server 'arbox': connected, 12 tools registered
```

---

## 4. Test through the LLM

Through `@nivoshaybot` in Telegram (or whatever channel nanobot is
attached to), say something like:

> Use the arbox tool to tell me what I'm booked into this week.

The LLM should call `mcp_arbox_arbox_bookings` and respond with
something like:

> You're on the waitlist for CrossFit Hall A tomorrow at 09:00
> (waitlist position 3 of 8). No other bookings this week.

For an end-to-end book flow:

> Preview a booking for tomorrow 09:00 Weightlifting Hall B.

The LLM should call `mcp_arbox_arbox_book` with `confirm: false`, see
`actual_send: false` in the response, and surface the preview. If you
then say "yes, book it", it should call again with `confirm: true`.

---

## 5. Suggested system prompt snippet

When the agent you're wiring up has its own system prompt, include
something like:

> You have access to the `arbox` tool suite for managing CrossFit class
> bookings. Read tools (`arbox_status`, `arbox_bookings`,
> `arbox_classes`, etc.) are safe and idempotent. Mutation tools
> (`arbox_book`, `arbox_cancel`, `arbox_pause`, `arbox_resume`) are
> dry-run by default — the response will include `"actual_send": false`.
> Always surface the dry-run preview to the user and only retry with
> `confirm: true` after the user explicitly confirms.

---

## 6. Security checklist

- **Loopback-only default on both hops.** The daemon binds to
  `127.0.0.1:8080`; `arbox-mcp` talks to that loopback address; nanobot
  talks to `arbox-mcp` over stdio (not a network). Nothing is reachable
  from outside the VM. Verify with
  `ssh oracle-vps 'ss -lntp | grep 8080'` — the owner should be `arbox`
  and the local address `127.0.0.1:8080`.
- **Token kind matters.** The default wiring uses
  `ARBOX_API_READ_TOKEN`. Swap to the admin token **only** if you want
  the LLM to book/cancel/pause. Even then, every mutation requires the
  tool-call arg `confirm: true` (the shim maps that to the daemon's
  `?confirm=1` gate).
- **Rotate tokens** on a schedule: regenerate in `~/arbox/data/.env`,
  update `~/.nanobot/secrets.env`, restart both units.
- **Audit log.** Every mutation writes a JSON line to
  `~/arbox/data/audit.jsonl` with `client_ip`, token kind, endpoint,
  `confirm` flag, and the request body. Readable via the
  `arbox_audit` route (not currently in `enabled_tools` to keep the
  LLM's tool surface focused on action; trivially added when you need
  it).
- **Rate limit.** 60 req/min per token, burst 30, enforced at the REST
  layer — applies to the shim like any other client.

---

## Appendix A — `arbox-mcp` internals

Source: [`cmd/arbox-mcp/`](../cmd/arbox-mcp). Two files: `main.go`
(transport + dispatch) and `tools.go` (tool catalog). Single-binary,
no dependencies beyond the Go stdlib. Tests in `main_test.go` cover
initialize, tools/list, tools/call (GET + mutation dry-run + mutation
confirm), upstream error surfacing, and unknown-method handling.

Protocol scope:

- MCP version advertised: `2025-06-18`.
- Methods: `initialize`, `initialized` (notif), `tools/list`,
  `tools/call`, `ping`, `shutdown`. Everything else returns
  `-32601 method not found` so unknown traffic stays visible.
- Transport: newline-delimited JSON on stdin/stdout.
- Mutation gate: the `confirm` tool-call argument moves to `?confirm=1`
  on the URL (REST contract) and is stripped from the JSON body. Any
  other argument goes in the body (POST) or query string (GET).

Upstream REST errors (non-2xx) surface as MCP tool-level errors
(`isError: true` on the result) rather than protocol errors, so a bad
call doesn't tear down the whole MCP session.
