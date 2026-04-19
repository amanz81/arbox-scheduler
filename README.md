# arbox-scheduler

A Go CLI + always-on daemon that **auto-books your weekly classes on
[Arbox](https://arboxapp.com/)** the moment the booking window opens.
Designed to deploy as a tiny Fly.io worker (or run on any Linux host) and
controlled end-to-end from **Telegram**.

- 24h booking window for most weekdays, **48h on Sunday** — handled per class.
- **Proactive scheduler:** wakes ~8 s before each window, then retries every
  second until `BOOKED`, `WAITLIST`, or 45 s timeout. Beats sub-10-second
  sellouts.
- **Telegram bot** for status, weekly setup wizard, pause/resume, self-test.
- Works for any Arbox member; the gym, weekly plan, and class filters are
  all in `config.yaml` (or env vars).

> Designed and built with one specific gym in mind, but **everything
> user-specific is parameterised** — see [Multi-user variables](#multi-user-variables).

---

## Table of contents

1. [What it does](#what-it-does)
2. [Quick start (local)](#quick-start-local)
3. [Quick start (Fly.io)](#quick-start-flyio)
4. [Configuration](#configuration)
5. [Telegram commands](#telegram-commands)
6. [Multi-user variables](#multi-user-variables)
7. [Architecture in 30 seconds](#architecture-in-30-seconds)
8. [Tuning + rate limits](#tuning--rate-limits)
9. [Security](#security)
10. [Development](#development)

---

## What it does

You declare a weekly plan like _"Sunday 08:00 prefer Hall B, fall back to
Hall A; Monday 08:30 same; rest day Wed/Sat"_.

The daemon:

1. Computes when each class's booking window opens (24h before, **48h** for
   Sunday classes).
2. Sleeps until ~8 s before that moment, refreshes the auth token, then
   **bursts up to 45 s of attempts at 1-second intervals** until the slot is
   booked or waitlisted.
3. Sends every outcome to your Telegram chat (booked / waitlisted / failed).
4. Once a day, sends a **heartbeat** with self-test results + the next 3
   bookings it plans to do.

You can pause it (`/pause 3d`), check live availability (`/morning`,
`/evening`), edit your plan from buttons (`/setup`), and inspect the deploy
(`/version`, `/selftest`).

---

## Quick start (local)

Requires Go **1.24+** and an Arbox _member_ account (the same email +
password you use in the Arbox app).

```bash
# 1. Build
go build -o bin/arbox ./cmd/arbox

# 2. Configure
cp .env.example .env
$EDITOR .env            # set ARBOX_EMAIL / ARBOX_PASSWORD (and ARBOX_GYM if you have multiple gyms)
$EDITOR config.yaml     # set timezone, gym substring, weekly plan, category_filter

# 3. Log in (writes ARBOX_TOKEN / ARBOX_REFRESH_TOKEN / box ids to .env)
./bin/arbox auth login

# 4. Verify everything is healthy
./bin/arbox selftest

# 5. Inspect what it would do
./bin/arbox schedule list --days 7
./bin/arbox schedule resolve --days 7   # online; resolves against live Arbox classes

# 6. Run the daemon
./bin/arbox daemon --interval 1m --lookahead 7
```

The daemon stays in the foreground; press `Ctrl-C` to stop it. To run it
unattended use systemd / launchd, **or** deploy to Fly.io (next section).

---

## Quick start (Fly.io)

Cost: **$0/month** within the Fly Hobby plan (one shared-cpu-1x VM, 256 MB,
1 GB volume, no public ports).

Detailed walkthrough lives in [`docs/DEPLOY-FLY.md`](docs/DEPLOY-FLY.md).
The 60-second version:

```bash
# 0. flyctl + sign-in (once per machine)
brew install flyctl
fly auth signup

# 1. Launch from this repo (don't deploy yet — secrets first)
fly launch --no-deploy --copy-config --name <your-app-name> --region fra

# 2. Persistent volume for tokens / user_plan / pause / booking_attempts
fly volumes create arbox_data --region fra --size 1

# 3. Secrets — Arbox login + (optional) Telegram + (optional) gym disambiguation
fly secrets set \
  ARBOX_EMAIL='you@example.com' \
  ARBOX_PASSWORD='your-arbox-password' \
  ARBOX_GYM='CrossFit Downtown' \
  TELEGRAM_BOT_TOKEN='123456:ABC...' \
  TELEGRAM_CHAT_ID='123456789'

# 4. Edit fly.toml: set `app = ` to your app name; tweak `primary_region`
#    if Frankfurt isn't closest to your gym.

# 5. Deploy
fly deploy

# 6. Verify
fly logs                       # see [proactive] / [tick] / [booker] lines
fly ssh console -C '/app/arbox selftest'
```

After this, control the app entirely from Telegram (see next section).

---

## Configuration

Two files matter:

- **`config.yaml`** — checked into the repo. Edit it once when you set up.
- **`.env`** — secrets. Gitignored. On Fly it lives at `/data/.env` (auto-created on first login).

### `config.yaml` — minimum viable

```yaml
timezone: Asia/Jerusalem      # or your gym's TZ
default_time: "09:00"

# REQUIRED if your Arbox account belongs to >1 gym; substring of the box or
# location name. Can also be set via ARBOX_GYM env (Fly secret).
gym: ""

# Only classes whose category contains an `include` AND no `exclude`
# substring are considered. Match is case-insensitive against the title
# Arbox returns (Latin or non-Latin scripts both work).
category_filter:
  include: ["WOD", "Workout"]
  exclude: ["Open Box", "Kids", "Teens"]

days:
  sunday:
    enabled: true
    options:
      - { time: "08:00", category: "Hall B" }    # priority 0 — preferred
      - { time: "08:00", category: "Hall A" }    # priority 1 — fallback
  monday:    { enabled: true, time: "08:30" }
  tuesday:   { enabled: true, time: "09:00", category: "Weightlifting" }
  wednesday: { enabled: false }                  # rest day
  thursday:  { enabled: true, time: "08:30" }
  friday:    { enabled: true, time: "11:00" }
  saturday:  { enabled: false }
```

`category` is a substring; first matching live class is used. Per-day
priority list: index 0 is most preferred. Daemon tries 0 first, falls back
to 1, etc., **inside the burst** (every 1 s for up to 45 s).

### Generating the plan from the gym (recommended)

Instead of hand-editing `days:`, run **`/setup`** in Telegram once the
daemon is up. It pulls the actual class list per weekday and lets you
toggle slots with inline ✓/○ buttons. **`/setupdone`** writes the result
to `user_plan.yaml` on the volume; the daemon merges it on every tick — no
restart needed.

---

## Telegram commands

`setMyCommands` registers all of these in the in-Telegram `/` menu on boot:

| Command | What it does |
|---|---|
| `/start`, `/help` | One-shot intro |
| `/status` | Saved selections per weekday + your real Arbox bookings |
| `/morning [HH-HH] [days\|week]` | Live class list, default 06–12 next 1 day |
| `/evening [HH-HH] [days\|week]` | Live class list, default 16–22 next 1 day |
| `/setup` | Inline buttons: pick weekly slots from real Arbox classes |
| `/setupdone` | Save `/setup` selections to `user_plan.yaml` |
| `/setupcancel` | Discard the in-progress `/setup` session |
| `/pause [Nh\|Nd\|until DATE [HH:MM]] [reason]` | Stop auto-booking (default 24h) |
| `/resume` | Resume auto-booking |
| `/version` | Build rev + gym + TZ + locations_box_id + pause state |
| `/selftest` | 8 health checks + next 3 scheduled bookings |

Notifications you'll receive automatically:

- `🟢 *Online*` — daemon boot
- `🫀 *Heartbeat*` — once per local calendar day with self-test + upcoming bookings
- `✅ *Booked*` / `⏳ *Waitlisted*` / `❌ *Booking failed*` — every booking attempt
- `🔴 *Shutting down*` — on SIGTERM (e.g. Fly redeploy)
- `⚠️ *Daemon error*` — for tick or booker errors

Only the chat id matching `TELEGRAM_CHAT_ID` is allowed to send commands.

---

## Multi-user variables

Everything user/gym-specific is one of:

| Where | Field | What it controls | How to override |
|---|---|---|---|
| `config.yaml` | `timezone` | Gym wall clock | `ARBOX_TIMEZONE` env |
| `config.yaml` | `gym` | Pick the right gym when account has many | `ARBOX_GYM` env |
| `config.yaml` | `category_filter.include` / `exclude` | Substring filter (Hebrew/English/etc.) | edit YAML |
| `config.yaml` | `days` / `default_time` | Weekly plan | edit YAML or use `/setup` → `user_plan.yaml` |
| `.env` | `ARBOX_EMAIL`, `ARBOX_PASSWORD` | Member login | Fly secrets |
| `.env` | `ARBOX_TOKEN`, `ARBOX_REFRESH_TOKEN` | Auto-managed | – |
| `.env` | `ARBOX_BOX_ID`, `ARBOX_LOCATIONS_BOX_ID` | Discovered (re-discovered if `gym:` is set) | – |
| `.env` | `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Telegram control | Fly secrets |
| `fly.toml` | `app`, `primary_region` | Fly app identity / region | edit and `fly deploy` |
| `fly.toml` | `[mounts].source` | Volume name | edit + `fly volumes create` |
| `Dockerfile` | `ENV TZ` | Process timezone | leave default; `ARBOX_TIMEZONE` overrides config |

Things that are **constants in code** but easy to flag if you want them as
config later:

- `proactiveLead` (8 s), `proactiveStrikeOffset` (250 ms) — `cmd/arbox/proactive.go`
- `burstDuration` (45 s), `burstInterval` (1 s), `burstFetchEvery` (5) — `cmd/arbox/burst.go`
- `scheduleCacheTTL` (30 s) — `cmd/arbox/schedule_cache.go`
- `setupHorizonDays` (14) — `cmd/arbox/telegram_setup_flow.go`
- daemon `--interval` (default 5 m) and `--lookahead` (default 7 d) are CLI flags

If you adapt this for a different gym chain or a different booking
provider, those are the only constants likely to need rebalancing.

---

## Architecture in 30 seconds

```
┌──────────────────────────────────────────────────────────────────────┐
│  arbox daemon (one process, one Fly machine)                         │
│                                                                      │
│  ┌─ Telegram bot (long-poll)  ──► /status /morning /setup /pause …   │
│  ├─ 5-min heartbeat ticker    ──► alive log + safety-net booker      │
│  └─ Proactive scheduler       ──► sleep until next WindowOpen,       │
│                                    burst BookClass every 1s for 45s  │
│                                                                      │
│  ┌─ /data/.env                 — tokens (auto-renewed on 401)        │
│  ├─ /data/user_plan.yaml       — overlay from /setup                 │
│  ├─ /data/pause.json           — /pause state                        │
│  └─ /data/booking_attempts.json — terminal results, prevents dup     │
└──────────────────────────────────────────────────────────────────────┘

           Arbox member API  apiappv2.arboxapp.com
           (login, schedule/betweenDates, scheduleUser/insert,
            standBy/insert, boxes/locations, boxes/<id>/memberships/1)
```

No public HTTP port. Outbound only. State is on a 1 GB Fly volume.

---

## Tuning + rate limits

We don't have a published Arbox rate limit, so the defaults are
deliberately conservative. Order of magnitude:

- **Idle hour:** ~12–24 GETs (5-min ticks).
- **Active hour with a few Telegram pulls:** ~24–40 GETs (a 30 s schedule
  cache de-dups repeated `/status`).
- **Booking-window peak (45 s burst):** ~9 GETs + up to ~45 POSTs.

Levers if you want to go gentler:

- `--interval 5m` (daemon flag) — increase to lower steady-state.
- `burstFetchEvery 5` (constant) — increase to fetch less often.
- `scheduleCacheTTL 30s` — increase if Telegram queries spam.

Or stricter — just say so and we can wire a token-bucket on
`arboxapi.Client` (60/min hard cap with adaptive backoff on 429).

---

## Security

- `.env` and any token/state files (`pause.json`, `user_plan.yaml`,
  `booking_attempts.json`) are written `0600` by the binary.
- On Fly, secrets are stored encrypted by Fly itself; `.env` lives only on
  the persistent volume, not in the image.
- The Telegram bot ignores any chat id other than `TELEGRAM_CHAT_ID`.
- The booking machinery is gated by `bookerMu` so the proactive goroutine
  and the safety-net ticker can never fire `BookClass` for the same
  `schedule_id` simultaneously, and `booking_attempts.json` makes terminal
  outcomes survive process restarts.
- The repo never contains personal gym names, phone numbers, or chat ids;
  use `ARBOX_GYM` and Fly secrets for that data.

---

## Development

```bash
go test ./...                       # full suite
go build -o bin/arbox ./cmd/arbox

# Useful local CLI
./bin/arbox classes list --days 2
./bin/arbox schedule resolve --days 7
./bin/arbox book class --schedule-id <N>      # default dry-run
./bin/arbox book class --schedule-id <N> --send
./bin/arbox selftest
```

Repo layout:

```
cmd/arbox/             # cobra CLI + Telegram bot + daemon
internal/arboxapi/     # member API client (login, schedule, book, waitlist)
internal/config/       # YAML load + validate + env overrides
internal/schedule/     # window math (24h / Sunday-48h, DST-correct)
internal/notify/       # Telegram + stdout notifiers
docs/DEPLOY-FLY.md     # detailed Fly walkthrough
```

PRs welcome. Tests must pass and stay deterministic (no fixed-date assumptions).
