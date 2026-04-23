# Arbox Auto-Scheduler — Project Prompt

> **📌 Historical design doc, not the source of truth.**
> This file captured the project's intent mid-2026 when Fly.io was still the
> deploy target. Fly was retired after Cloudflare started 403'ing its ASN
> from the Arbox API. For **current** state and operations see:
>
> - [`README.md`](README.md) — user-facing overview + quick start
> - [`docs/FORK-SETUP.md`](docs/FORK-SETUP.md) — "I want to run this" walkthrough
> - [`docs/DEPLOY-ORACLE.md`](docs/DEPLOY-ORACLE.md) — production Oracle VM runbook
> - [`.cursor/skills/arbox-scheduler/SKILL.md`](.cursor/skills/arbox-scheduler/SKILL.md) — the skill an LLM should load when editing this repo
>
> Content below references `/data/`, `fly.toml`, `fly secrets`, etc. — none
> of those are accurate for the current layout. Preserved for commit
> archaeology only.

---

This document captured the **state and intent** of the project so a
new Claude Code session (or a new contributor) could pick up where we left off
without re-discovering decisions. The original day-1 prompt is preserved at
the bottom under [Phase-1 prompt (historical)](#phase-1-prompt-historical).

---

## What this project is

A Go CLI + always-on daemon that **auto-books your weekly classes on
[Arbox](https://arboxapp.com/)** the moment the booking window opens.
Designed to deploy as a tiny Fly.io worker (or run on any Linux host) and
controlled end-to-end from a **Telegram bot**.

- 24h booking window for most weekdays, **48h on Sunday** — handled per class.
- **Proactive scheduler:** wakes ~8 s before each window, then bursts
  attempts every second until `BOOKED`, `WAITLIST`, or 45 s timeout. Built to
  beat sub-10-second sellouts.
- All user-specific values are parameters (`config.yaml` + env overrides).

The repo started with one specific user in mind but is now **generic
enough that anyone with an Arbox member account can clone, configure, and
deploy**. See `README.md` for the user-facing setup.

---

## Status (phase tracker)

| Phase | Goal | Status |
|---|---|---|
| 1 | Config schema, `schedule list/validate`, booker stub | ✅ done |
| 2 | Arbox member API client (auth, list, book, waitlist, cancel) | ✅ done |
| 3 | Scheduler daemon — proactive strike at window-open + 45 s burst | ✅ done |
| 4 | Fly.io deploy (Dockerfile, fly.toml, persistent volume, GitHub Actions) | ✅ done |
| 5 | Telegram bot (status, setup wizard, pause/resume, version, selftest) | ✅ done |
| 6 | LLM/MCP API surface (HTTP REST + bearer for nanobot) | ✅ done |
| 7 | Multi-tenant (one process serves several Arbox accounts) | 🔜 not started |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  arbox daemon (one process, one Fly machine)                        │
│                                                                     │
│  ┌─ Telegram bot (long-poll)  ──► /status /morning /setup /pause …  │
│  ├─ 5-min heartbeat ticker    ──► alive log + safety-net booker     │
│  └─ Proactive scheduler       ──► sleep until next WindowOpen,      │
│                                    burst BookClass every 1s for 45s │
│                                                                     │
│  ┌─ /data/.env                 — tokens (auto-renewed on 401)       │
│  ├─ /data/user_plan.yaml       — overlay from /setup                │
│  ├─ /data/pause.json           — /pause state                       │
│  └─ /data/booking_attempts.json — terminal results, prevents dup    │
└─────────────────────────────────────────────────────────────────────┘

  Arbox member API  apiappv2.arboxapp.com
  Endpoints used:
    POST /api/v2/user/login                 — login + refresh token
    GET  /api/v2/boxes/locations            — discover gym + locations_box_id
    GET  /api/v2/boxes/<box_id>/memberships/1 — membership_user_id
    POST /api/v2/schedule/betweenDates      — list classes (one day at a time, UTC midnight)
    POST /api/v2/scheduleUser/insert        — book a class (confirmed)
    POST /api/v2/scheduleUser/cancel        — cancel  (best-guess endpoint)
    POST /api/v2/standBy/insert             — join waitlist (best-guess)
    POST /api/v2/standBy/cancel             — leave waitlist (best-guess)
    GET  /api/v2/user/feed                  — past/future booking counters
```

No public HTTP port. Outbound only. State lives on a 1 GB Fly volume.

---

## Repo layout (current)

```
cmd/arbox/
  main.go                  — cobra root + subcommands wiring
  daemon.go                — long-running supervisor + 5-min ticker
  proactive.go             — sleeps to next WindowOpen, triggers burst
  burst.go                 — per-slot retry loop (1s × 45s, priority-aware)
  booker.go                — single-shot booker (used by ticker safety net)
  selftest.go              — `arbox selftest` + /selftest health checks
  version.go               — `/version` report (build rev + gym + TZ)
  pause.go                 — /pause + /resume + persisted state
  classes.go               — `arbox classes list` + ensureLocationsBoxID
  book.go                  — `arbox book class/cancel/waitlist`
  me.go                    — `arbox me` (membership info)
  auth.go                  — `arbox auth` (login, token refresh)
  schedule_resolve.go      — `arbox schedule resolve` + /status / /weekly
  schedule_cache.go        — 30s TTL cache for Telegram queries
  morning.go               — /morning + /evening live class window views
  setup_candidates.go      — build setup options from real Arbox classes
  setup_session.go         — read/write /setup session JSON
  telegram_setup_flow.go   — /setup inline-keyboard flow
  telegram_bot.go          — Telegram long-poll dispatcher + tgSetMyCommands
  telegram_cmd.go          — `arbox telegram` CLI subtree
  paths_data.go            — paths to .env / pause / user_plan / attempts
  class_filter.go          — global include/exclude rules
internal/
  arboxapi/                — HTTP client (login, schedule, book, waitlist, …)
  config/                  — YAML load + Validate + ARBOX_GYM/ARBOX_TIMEZONE env overrides
  schedule/                — window math (24h, Sunday-48h, DST-correct)
  notify/                  — Notifier interface + Telegram + stdout impls
  envfile/                 — read/write .env (Upsert)
docs/
  DEPLOY-FLY.md            — full Fly.io walkthrough
  blue-green-fkp-migration.md
README.md                  — user-facing intro + quick starts
config.yaml                — committed template; gym field is empty placeholder
.env.example               — every recognized env var, documented
fly.toml                   — Fly app config (no public ports)
Dockerfile                 — multi-stage; final image ~20 MB on alpine
.dockerignore
.github/workflows/         — CI + Fly deploy on push to main
arbox-scheduler-prompt.md  — this file
```

---

## Configuration model

Two files matter:

- **`config.yaml`** — committed; per-gym/per-user fields are **empty** so
  the repo stays generic. Edit before first run, or override with env vars.
- **`/data/.env`** (Fly) or **`./.env`** (local) — secrets and discovered
  IDs. Gitignored. Auto-managed: only `ARBOX_EMAIL` / `ARBOX_PASSWORD`
  must be set by hand.

### Config fields (`config.yaml`)

| Field | Required | Override env | Notes |
|---|---|---|---|
| `timezone` | yes | `ARBOX_TIMEZONE` | e.g. `Asia/Jerusalem` |
| `default_time` | no | – | Fallback HH:MM for enabled days without an explicit time |
| `gym` | yes if account has >1 gym | **`ARBOX_GYM`** | Case-insensitive substring; matched against box name + location name |
| `category_filter.include` | yes (or empty list) | – | YAML list **or** comma-separated string; case-insensitive substrings on Arbox class titles |
| `category_filter.exclude` | no | – | Same shape; runs first |
| `days.<weekday>.enabled` | yes | – | `false` = rest day |
| `days.<weekday>.time` / `category` | shorthand | – | Single option |
| `days.<weekday>.options[]` | full form | – | Priority list, index 0 = most preferred |

### Recognized env vars (`.env` / Fly secrets)

| Var | Purpose |
|---|---|
| `ARBOX_EMAIL` | Member login email |
| `ARBOX_PASSWORD` | Member password |
| `ARBOX_TOKEN`, `ARBOX_REFRESH_TOKEN` | Auto-managed by `auth.go` |
| `ARBOX_BOX_ID`, `ARBOX_LOCATIONS_BOX_ID` | Discovered; re-discovered when `ARBOX_GYM` is set |
| `ARBOX_BASE_URL` | Override API host (rarely needed) |
| `ARBOX_GYM` | Pick gym in multi-gym accounts (overrides `gym:` in YAML) |
| `ARBOX_TIMEZONE` | Override `timezone:` in YAML |
| `ARBOX_ENV_FILE` | Path to `.env` (default `./.env`; on Fly: `/data/.env`) |
| `ARBOX_USER_PLAN` | Path to `user_plan.yaml` overlay |
| `ARBOX_SETUP_SESSION` | Path to `setup_session.json` |
| `ARBOX_PAUSE_STATE` | Path to `pause.json` |
| `ARBOX_BOOKING_ATTEMPTS` | Path to `booking_attempts.json` |
| `TELEGRAM_BOT_TOKEN` | Bot token from @BotFather |
| `TELEGRAM_CHAT_ID` | Numeric chat id allowed to control the bot |
| `TZ` | Process timezone (Dockerfile sets `Asia/Jerusalem`) |

### Code-level constants (tunable; not yet flags)

- `proactiveLead` (8 s), `proactiveStrikeOffset` (250 ms), `proactiveMaxSleep` (1 h) — `cmd/arbox/proactive.go`
- `burstDuration` (45 s), `burstInterval` (1 s), `burstFetchEvery` (5) — `cmd/arbox/burst.go`
- `scheduleCacheTTL` (30 s) — `cmd/arbox/schedule_cache.go`
- `setupHorizonDays` (14) — `cmd/arbox/telegram_setup_flow.go`
- daemon `--interval` (5 m) and `--lookahead` (7 d) are CLI flags

---

## Telegram bot

### Single source of truth

`telegramCommandList` in `cmd/arbox/telegram_bot.go` is the one place
commands are declared. `tgSetMyCommands` iterates it. A regression test
(`TestTelegramCommandsConsistent`) parses the dispatcher switch and asserts
**symmetric equality** — adding to one without the other fails CI.

### Commands

| Command | Description |
|---|---|
| `/start`, `/help` | One-shot intro |
| `/status` | Saved selections per weekday + your real Arbox bookings |
| `/morning [HH-HH] [days\|week]` | Live class list, default 06–12 next 1 day |
| `/evening [HH-HH] [days\|week]` | Live class list, default 16–22 next 1 day |
| `/setup` | Inline buttons: pick weekly slots from real Arbox classes |
| `/setupdone` | Save `/setup` selections to `user_plan.yaml` |
| `/setupcancel` | Discard the in-progress `/setup` session |
| `/pause [Nh\|Nd\|until DATE [HH:MM]] [reason]` | Stop auto-booking (default 24h, max 90d) |
| `/resume` | Resume auto-booking |
| `/version` | Build rev + gym + TZ + locations_box_id + pause state |
| `/selftest` | 8 health checks + next 3 scheduled bookings |

### Notifications (outbound)

- `🟢 Online` on boot
- `🫀 Heartbeat` once per local calendar day (includes self-test + next bookings)
- `✅ Booked` / `⏳ Waitlisted` / `❌ Booking failed` per attempt
- `🔴 Shutting down` on SIGTERM
- `⚠️ Daemon error` on tick / booker errors

Only `TELEGRAM_CHAT_ID` may send commands; everything else is logged and ignored.

---

## Booking machinery

### Proactive scheduler (the main path)

`runProactiveBooker` (`cmd/arbox/proactive.go`) runs forever in a goroutine
started from the daemon:

1. Find the earliest upcoming `WindowOpen` across all `PlannedOption`s.
2. Sleep in chunks (max 1 h) so config / pause edits take effect.
3. ~8 s before the window: refresh config, check `/pause`, fire an auth
   probe so a stale token re-logins **before** the strike.
4. Sleep until `WindowOpen − 250 ms` (compensate for clock skew + RTT).
5. Acquire `bookerMu`, call `bookSlotBurst` for every `ClassStart` whose
   `WindowOpen == nextWin` (Sunday Hall A + Hall B share a `ClassStart`).
6. 750 ms breather, loop.

### `bookSlotBurst` (the strike)

`cmd/arbox/burst.go` — per slot, for up to **45 s** at **1 s** intervals:

1. Re-fetch the day's classes every **5th** attempt (cached in between).
2. Skip if user already holds any class at that exact `ClassStart`.
3. Walk the priority list:
   - `cl.Free > 0` → `BookClass`. Success → notify (`EventBooked`), persist, **stop**.
   - Else → `JoinWaitlist`. Success → notify (`EventWaitlisted`), persist, **stop**.
   - Else → next priority option this attempt.
4. Burst exhausted → notify `EventFailed` with attempt count + last Arbox message.

Persistent dedupe in `/data/booking_attempts.json` (per `schedule_id`,
keyed terminal: `BOOKED` / `WAITLIST` / `FAILED`); pruned after 30 days.

### Safety-net ticker

Every `--interval` (5 m by default) the daemon also calls `runBooker`
(single shot per slot whose window is currently open). Same `bookerMu`
and same attempts file — covers windows missed during a process restart.

### Concurrency

`bookerMu` is a single package-level mutex held by **both** the proactive
goroutine and the safety-net ticker. `BookClass` for the same `schedule_id`
can never fire twice in parallel.

---

## Schedule fetch — the off-by-one we tripped on

`GetScheduleDay` **must** send `YYYY-MM-DDT00:00:00.000Z` (UTC midnight of
the calendar date). Sending IL midnight (`= prior-day 21:00 UTC`) caused
Sunday lookups to return Saturday rows. There is a regression test
(`TestGetScheduleDay_SendsUTCMidnight`).

`fetchScheduleWindow` re-buckets every returned class by its own
`class.Date`, with seen-by-id dedupe — so even if the API returns adjacent
days, every row lands under the right key.

Time string normalization: `hhmm()` (`cmd/arbox/schedule_resolve.go`) trims
`:SS` and zero-pads single-digit hours so `08:00` matches `08:00:00`,
`8:00`, etc.

---

## Self-test (`/selftest` and `arbox selftest`)

`runSelfTest` in `cmd/arbox/selftest.go` runs 8 probes with name + ✓/✗ +
latency + detail:

1. Config + TZ
2. Pause state
3. Locations API (auth + correct gym binding)
4. Membership lookup
5. Schedule fetch (today)
6. Booking attempts file readable + writable
7. Plan resolves at least one upcoming option
8. Schedule cache stats

The daily heartbeat appends both the self-test output **and** the next 3
planned bookings (`nextPlannedBookingsSummary` collapses priorities per
slot: `Hall B then Hall A · window Fri 24 Apr 08:00 (in 6h)`).

---

## Rate limits / API budget

We don't have a published Arbox limit; defaults are conservative:

- **Idle hour:** ~12–24 GETs (5-min ticks).
- **Active hour with Telegram pulls:** ~24–40 GETs (30 s schedule cache de-dups repeats).
- **Booking-window peak (45 s burst):** ~9 GETs + up to ~45 POSTs.

If we ever hit `429` we'd add a token-bucket on `arboxapi.Client` (60/min
hard cap) + adaptive backoff. Not built yet.

---

## Deployment (Fly.io)

`docs/DEPLOY-FLY.md` is the full walkthrough; **README.md** has the 60-second
version. Highlights:

- Single shared-cpu-1x VM (256 MB) + 1 GB volume — fits the Hobby plan ($0).
- Frankfurt region (lowest latency to Israel; choose closer if elsewhere).
- Persistent volume mounted at `/data`.
- Secrets (`ARBOX_EMAIL`, `ARBOX_PASSWORD`, `ARBOX_GYM`, Telegram tokens)
  set via `fly secrets set`; **never** in the image.
- Auto-stop **OFF** so the proactive scheduler can fire at any time.
- GitHub Actions workflow auto-deploys on push to `main`.

---

## Open / planned work

1. **MCP stdio shim (`cmd/arbox-mcp/`)** — only ship if Path A
   (HTTP MCP via OpenAPI tool discovery, see `docs/NANOBOT.md`) turns
   out awkward in practice. The HTTP API itself is built and live;
   see [`docs/API.md`](docs/API.md).
2. **Boot-time validation** that refuses to start the daemon if the
   account has multiple boxes and `ARBOX_GYM` is unset.
3. **Cancel-then-rebook** if a higher-priority slot frees up after a
   lower-priority booking.
4. **`arbox init` interactive bootstrap** for non-technical users.
5. **Verify standby endpoints** (`/api/v2/standBy/insert` and `/cancel`)
   against real traffic — currently best-guess.
6. **Adaptive 429 backoff** on `arboxapi.Client`.

---

## Ground rules (still apply)

- **Go**, single binary. Standard library where reasonable; small focused
  deps (`spf13/cobra`, `gopkg.in/yaml.v3`).
- **No premature abstractions.** No framework bloat.
- **Tests** must pass and stay deterministic — no fixed-date assumptions.
  When a test needs a future Sunday, compute it from `time.Now()`.
- **No secrets in the repo.** All credentials via Fly secrets / `.env`.
  The committed `config.yaml` has empty placeholders for personal fields.
- **Do not delete files or working code without an explicit reason.**
  Wrap with conditionals or new branches; preserve existing behavior.

---

## Phase-1 prompt (historical)

The original prompt that bootstrapped the project. Kept for posterity —
the data model (`config.yaml` / per-day priority list) and the
24h/Sunday-48h booking windows are unchanged from this prompt.

> I want to build a CLI app in **Go** that automatically books my weekly
> CrossFit classes on **Arbox** as soon as the booking window opens.
>
> **Arbox booking windows:** Most days open **24h** before class start;
> **Sunday** opens **48h** before. **Timezone:** Asia/Jerusalem.
>
> **Behavior:** When the booking window opens, fire the booking request
> immediately. If the class is full, auto-join the waiting list and
> notify me. If booking succeeds, notify me. Config editable without
> rebuilding the binary.
>
> **First-pass scope (no Arbox API yet):** `config.yaml` with weekly
> schedule, `arbox schedule list` (next 7 days + window-open times),
> `arbox schedule validate`, booker stub, Notifier interface (stdout impl).
>
> **Later phases:** API client → daemon → VPS deploy.
>
> **Ground rules:** Go single binary, small focused deps, unit tests for
> window math (DST + Sunday exception), no secrets in the repo.

The fallback-class mechanism the original prompt explicitly excluded
("no try-the-next-slot") was **later added** as the priority list inside
`days.<weekday>.options[]`, because in practice the gym's hall-A/hall-B
distinction matters and a one-shot strike on a sold-out slot misses too
often. The 1-second burst loop replaces "fire exactly once at window-open."
