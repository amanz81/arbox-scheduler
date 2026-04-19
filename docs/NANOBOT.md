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

## 1. Set the API token on the nanobot host

Pick **one** of:

```bash
# Read-only — safe for monitoring agents that should never act.
export ARBOX_API_TOKEN="$ARBOX_API_READ_TOKEN"

# Admin — required if the agent should book / cancel / pause.
export ARBOX_API_TOKEN="$ARBOX_API_ADMIN_TOKEN"
```

Even with the admin token, every mutation still requires `?confirm=1` on
the URL — the LLM has to deliberately add it before anything hits Arbox.

---

## 2. Add to nanobot config

Drop this into your nanobot config (typically `~/.config/nanobot/config.json`
or your project's `nanobot.json`):

```json
{
  "tools": {
    "mcpServers": {
      "arbox": {
        "type": "http",
        "url":  "https://your-app.fly.dev",
        "headers": {
          "Authorization": "Bearer ${ARBOX_API_TOKEN}"
        },
        "openapi": "https://your-app.fly.dev/api/v1/openapi.json"
      }
    }
  }
}
```

Replace `https://your-app.fly.dev` with your Fly app URL (or any other
public host where the daemon is running).

---

## 3. Test it

After restarting nanobot, the LLM should see tools like
`arbox.get_status`, `arbox.get_classes`, `arbox.post_book`,
`arbox.post_pause`, etc. (Exact tool names depend on nanobot's OpenAPI
import naming.)

Quick sanity:

```bash
nanobot tool list arbox
nanobot tool call arbox.get_version
nanobot tool call arbox.get_status --query days=7
```

If your nanobot CLI's exact subcommand differs, the same calls work as
plain HTTP from `curl`:

```bash
curl -H "Authorization: Bearer $ARBOX_API_TOKEN" \
  https://your-app.fly.dev/api/v1/version
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

- **Rotate tokens** on a schedule (`fly secrets set ARBOX_API_*_TOKEN=…` +
  redeploy).
- **Use the read token whenever possible.** Only give the agent the admin
  token if you actually want it to mutate.
- The two-tier separation means a leaked read token cannot book or cancel.
- Rate limit (60/min, burst 30) caps blast radius on a leaked token.
- Bearer over TLS only — Fly enforces HTTPS at the edge (`force_https =
  true` in `fly.toml`).
- The audit log records every mutation request with `client_ip`, so you
  can grep for unexpected origins.
- (Optional, future) IP allowlist via Fly's edge or a small middleware
  if you ever expose this beyond the LLM hosts.
