# Wiring arbox-scheduler into nanobot

[nanobot](https://github.com/nanobot-ai/nanobot) (and most other LLM
tool-calling clients) speak MCP over HTTP and discover tools from an
OpenAPI spec. The arbox-scheduler daemon serves OpenAPI 3.1 at
`/api/v1/openapi.json`; nanobot can register every endpoint as a tool
automatically.

This is **Path A**: HTTP MCP with OpenAPI-driven tool discovery. No stdio
shim needed. (A future `cmd/arbox-mcp/` could wrap the same routes in
stdio MCP if Path A turns out awkward in practice — not shipped here.)

---

## Production setup: same Oracle VM

The daemon and nanobot both run on the same Oracle Free Tier VM, as the
same `ubuntu` user, under systemd. The HTTP API binds to
**`127.0.0.1:8080`** by default — loopback only, nothing exposed to the
internet. nanobot calls it via `http://127.0.0.1:8080`, zero network
surface.

```
┌─ Oracle VM (ubuntu) ──────────────────────────────────────┐
│  arbox.service    ── daemon + /api/v1 on 127.0.0.1:8080   │
│  nanobot.service  ── python3.11 -m nanobot gateway        │
│       │                                                   │
│       └──── HTTP loopback ────► 127.0.0.1:8080/api/v1     │
└───────────────────────────────────────────────────────────┘
```

No firewall rules, no TLS cert, no public DNS needed.

---

## 1. Enable the API on the daemon side

Set the tokens in `~/arbox/data/.env` on the Oracle VM:

```bash
ssh oracle-vps 'cat >> ~/arbox/data/.env' <<EOF
ARBOX_API_READ_TOKEN=$(openssl rand -hex 32)
ARBOX_API_ADMIN_TOKEN=$(openssl rand -hex 32)
EOF
ssh oracle-vps 'sudo systemctl restart arbox'
ssh oracle-vps 'sudo journalctl -u arbox -n 5 --no-pager'   # expect "[http] listening on 127.0.0.1:8080"
```

Pick the two tokens carefully: the admin token can book/cancel/pause
anything; the read token is GET-only. Keep the admin token in a vault
entry; the read token is safe(r) to paste into nanobot logs.

Until **both** tokens are unset the HTTP server stays dark — the Go
boot logs `[http] disabled (no API tokens set; set ARBOX_API_READ_TOKEN
and/or ARBOX_API_ADMIN_TOKEN to enable)`.

Confirm it's up from the VM itself:

```bash
ssh oracle-vps 'curl -s http://127.0.0.1:8080/api/v1/healthz'
# → {"ok":true,"version":"94ecb16"}
```

---

## 2. Add to nanobot config

Edit `/home/ubuntu/.nanobot/config.*` (or the equivalent `nanobot.json`
your nanobot gateway loads) to register arbox as an MCP server. Use the
**read** token here unless you're deliberately giving the LLM mutation
rights:

```json
{
  "tools": {
    "mcpServers": {
      "arbox": {
        "type": "http",
        "url":  "http://127.0.0.1:8080",
        "headers": {
          "Authorization": "Bearer ${ARBOX_API_TOKEN}"
        },
        "openapi": "http://127.0.0.1:8080/api/v1/openapi.json"
      }
    }
  }
}
```

Set `ARBOX_API_TOKEN` in nanobot's `EnvironmentFile`
(`/home/ubuntu/.nanobot/secrets.env`) — nanobot's systemd unit already
loads that file. Example:

```bash
ssh oracle-vps 'cat >> ~/.nanobot/secrets.env' <<'EOF'
# Read token for arbox (GET-only). Use the admin token instead if you
# want the LLM to book / cancel / pause.
ARBOX_API_TOKEN=<paste ARBOX_API_READ_TOKEN value here>
EOF
ssh oracle-vps 'sudo systemctl restart nanobot'
```

Even with the admin token, every mutation still requires `?confirm=1`
on the URL — the LLM has to deliberately add it before anything hits
Arbox.

---

## 3. Test it

After restarting nanobot, the LLM should see tools like
`arbox.get_status`, `arbox.get_classes`, `arbox.post_book`,
`arbox.post_pause`, etc. (Exact tool names depend on nanobot's OpenAPI
import naming.)

Quick sanity from the Oracle VM:

```bash
nanobot tool list arbox
nanobot tool call arbox.get_version
nanobot tool call arbox.get_status --query days=7
```

If your nanobot CLI's exact subcommand differs, the same calls work as
plain HTTP from `curl`:

```bash
curl -H "Authorization: Bearer $ARBOX_API_TOKEN" \
  http://127.0.0.1:8080/api/v1/version
```

---

## 4. Suggested LLM prompt boilerplate

When wiring this into a Claude / OpenAI agent, include something like:

> You have access to the `arbox` tool. Read endpoints (status, classes,
> bookings, etc.) are safe and idempotent. Mutating endpoints (book,
> cancel, pause, plan) are dry-run by default — they return
> `actual_send: false` unless you add `?confirm=1` on the URL. Always
> show the dry-run preview to the user and ask for explicit confirmation
> before retrying with `?confirm=1`. Audit log is at `GET /api/v1/audit`.

---

## 5. Security checklist

- **Loopback-only default.** As long as `ARBOX_HTTP_ADDR` stays
  unset (or is `127.0.0.1:8080`), the API is not reachable from outside
  the VM. You can verify with `ss -lntp | grep 8080`.
- **Rotate tokens** on a schedule: generate new ones, update
  `~/arbox/data/.env` + `~/.nanobot/secrets.env`, restart both units.
- **Use the read token whenever possible.** Only hand nanobot the admin
  token if you actually want it to mutate.
- **Two-tier separation** means a leaked read token cannot book or
  cancel.
- **Rate limit** (60/min, burst 30) caps blast radius on a leaked token.
- **Audit log** records every mutation request with `client_ip`
  (`127.0.0.1` on the loopback path); anything else would mean
  `ARBOX_HTTP_ADDR` is set wider than intended.
- **Mutations require `?confirm=1`** on top of the admin token; dry-run
  is always the first call.
- If you ever need to expose this over a real network (e.g. a second
  Oracle VM running nanobot, or a remote LLM), put it behind a Unix
  socket (nanobot supports them) or add a TLS-terminating proxy with
  IP allowlist. Do not just flip to `ARBOX_HTTP_ADDR=:8080` and trust
  the bearer — the bearer token is fine, but you'd also need to open
  the VCN security list and that's an easy misconfiguration.
