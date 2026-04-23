---
description: arbox-scheduler — Go daemon that auto-books CrossFit/Arbox classes the instant a booking window opens. Reach for this when working in this repo.
---

# arbox-scheduler — orientation for LLMs

This skill loads when you work inside the `amanz81/arbox-scheduler`
repo. Use it to avoid relearning the architecture every session.

## What this project is

A single-process Go binary (`cmd/arbox`) that:

1. Logs into Arbox's **member API** at `apiappv2.arboxapp.com` (not the
   gym-owner panel — different backend).
2. Reads a weekly plan from `config.yaml` + an optional overlay in
   `user_plan.yaml` (set by Telegram `/setup`).
3. Runs a **proactive scheduler** that sleeps until ~8 s before each
   class's booking window opens (24 h before class for most weekdays,
   **48 h on Sunday**), then **bursts `BookClass` every 1 s for up to
   45 s** until booked, waitlisted, or timed out.
4. Surfaces state + commands over Telegram (long-poll bot).
5. Optionally exposes an HTTP REST API under `/api/v1/...` for LLM
   tool-calling clients. **Off by default** until tokens are set.

## Things that have been painful before

- **Telegram bot token is single-holder.** Two processes polling the
  same bot token → Telegram returns 409 Conflict on the loser.
  When swapping between nanobot / OpenClaw / dev iteration, stop the
  other one first.
- **Cloudflare bot-fight blocks hosting-ASN IPs.** Fly.io Frankfurt
  gets 403 + HTML challenge; Oracle Cloud and residential IPs pass.
  PR #3 added browser-impersonation headers (Safari UA + Origin/Referer
  matching `app.arboxapp.com`) which is what keeps us working on
  Oracle today. If a 403 lands on a new host, first check those headers
  are still applied (`internal/arboxapi/client.go` →
  `applyCommonHeaders`), THEN try a different ASN.
- **HTTP 514 from Arbox is terminal.** `medicalWavierRestricted` /
  `planNotAllowed` / `scheduleFullForMember` mean retrying won't help
  without user action. The booker has a 6-hour backoff for terminal
  failures; see `cmd/arbox/booker.go` → `isTerminalHTTP` +
  `bookingAttempt.Terminal`.
- **`alreadyHoldsAtStart` matches by TIME only**, not class identity.
  So if user is on waitlist for class A at 09:00 and plan wants class B
  at 09:00, the daemon skips the whole 09:00 slot. This is **intentional**
  — the user may want to manually manage overlap.
- **Arbox renames endpoints occasionally.** The 2026-04-20 rename was
  `/api/v2/standBy/insert` → `/api/v2/scheduleStandBy/insert`. PR #10
  adds the escape hatch: use `POST /api/v1/arbox/query` (admin bearer,
  `{method, path, body?}`) to GET-probe for route existence via 405
  vs 404 responses, then patch `internal/arboxapi/booking.go`.

## Architecture map (where to find stuff)

```
cmd/arbox/
  main.go                CLI entry + root command
  daemon.go              Tick loop + proactive scheduler wiring
  proactive.go           T-8s wakeup, strike timing
  burst.go               45 s × 1 Hz retry burst
  booker.go              runBooker — transient vs terminal classification
  telegram_bot.go        Long-poll command dispatcher
  schedule_resolve.go    Plan × live schedule resolver, shared builders
  http_api.go            /api/v1 server, loopback-default
  http_handlers.go       Per-route handlers (reads + mutations)
  http_auth.go           Two-tier bearer: READ vs ADMIN tokens
  http_arbox_query.go    Generic /api/v1/arbox/query passthrough
  http_audit.go          /data/audit.jsonl rotator + reader
  tg_preview.go          CLI helper to print exact Telegram output without sending
  booking_attempts.json  Persisted terminal outcomes (prevents retry storms)

cmd/arbox-mcp/           Separate binary: MCP stdio shim wrapping /api/v1.
  main.go                Protocol layer — initialize/tools.list/tools.call/shutdown
  tools.go               Hardcoded tool catalog (13 entries incl arbox_api_query)

internal/arboxapi/
  client.go              HTTP client with Cloudflare-impersonation headers
  schedule.go            /schedule/betweenDates client + Raw() passthrough
  booking.go             BookClass + Cancel + JoinWaitlist + LeaveWaitlist

internal/schedule/       Window math (24h / Sunday-48h, DST-correct)
internal/config/         YAML loader + validator + env overrides
internal/notify/         Telegram + stdout notifiers

scripts/deploy-oracle.sh Cross-compile + scp + systemctl restart + selftest smoke
docs/FORK-SETUP.md       First-time walkthrough for someone forking the repo
docs/DEPLOY-ORACLE.md    Production Oracle Cloud runbook
docs/API.md              /api/v1 endpoint reference
docs/NANOBOT.md          MCP wiring (applies to any native-MCP client)
```

## Testing + deploy workflow

- **Unit tests:** `go test ./...` — mandatory before any commit. Tests
  use `httptest.NewServer` to stub the Arbox upstream; no real network.
- **`./bin/arbox tg-preview /status`** — prints the exact Telegram body
  without sending a message. Fastest way to validate renderer changes.
- **`./bin/arbox selftest`** — 8 health checks; part of every deploy's
  smoke gate.
- **`bash scripts/deploy-oracle.sh`** — builds both `arbox` + `arbox-mcp`
  binaries, scps to `oracle-vps:~/arbox/bin/*.new`, atomic-swaps,
  `sudo systemctl restart arbox`, runs remote selftest. Fails the
  deploy if selftest isn't 8/8.
- **Branch protection on `main`:** PR required (0 reviewers, so the
  owner can self-merge), squash-merge only, linear history, no force
  push, require conversation resolution. Admin bypass enabled.

## Safety gates that must stay intact

- HTTP API refuses to start unless `ARBOX_API_READ_TOKEN` and/or
  `ARBOX_API_ADMIN_TOKEN` is set. Don't "helpfully" default them.
- Every mutation endpoint requires **admin bearer + `?confirm=1`**.
  Without `?confirm=1` the handler returns `{actual_send: false}` and
  writes a `confirm:false` audit line. Do not remove this gate.
- `/api/v1/arbox/query` passthrough has: admin bearer, method
  allowlist, path regex `^/api/v2/…`, `..` rejection, auth-route
  blocklist, audit-always. All five must remain.
- Bearer comparisons are constant-time (`subtle.ConstantTimeCompare`)
  in `http_auth.go`. Keep it that way if you refactor.

## Known open questions

- Arbox's member API has no documented public contract; endpoints
  drift. When something breaks, check the Arbox web app's network tab
  first, THEN grep the code.
- The Windows home PC as a host is a legit option but requires care
  around LAN exposure (prompt-injection attack surface into the home
  network). If that's the path, run OpenClaw inside a Hyper-V Linux VM
  with NAT-only networking, NOT directly on the Windows user.

## When the user says things like…

- **"book me into X tomorrow"** — run through the API
  `POST /api/v1/book?confirm=1 {"schedule_id":…}` (if arbox is up).
  Dry-run first without `?confirm=1` and show the preview.
- **"what's my waitlist position?"** — `GET /api/v1/bookings` now
  surfaces `stand_by_position` + `status_detail` (e.g. "WAITLIST 3/8")
  after PR #9. Before you say "I don't know", call this.
- **"why did booking fail?"** — `GET /api/v1/attempts?days=30` or read
  `~/arbox/data/booking_attempts.json` directly. 514 = terminal; 4xx =
  transient; look at `Message` field for Arbox's human-readable reason.

## Style

- Go code: idiomatic, small packages, no dependency sprawl. Current
  deps: `cobra`, `yaml.v3`, `golang.org/x/time/rate`, stdlib.
- Tests deterministic: no `time.Now()` in assertions — always pass an
  explicit time. No fixed-date data (e.g. "2026-04-21") in test logic.
- Comments explain **why** (tradeoffs, history, gotchas), not what.
- Commit messages: conventional style (`feat(x):`, `fix(x):`, `docs:`).
  Body explains the user-visible change and the tradeoff, not a file
  list.
