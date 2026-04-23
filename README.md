# arbox-scheduler

A Go CLI + always-on daemon that **auto-books your weekly classes on
[Arbox](https://arboxapp.com/)** the moment the booking window opens.
Runs on a tiny Linux host (current production target: **Oracle Cloud Free
Tier VM**, ~7 MB RSS) and is controlled end-to-end from **Telegram**.

- 24h booking window for most weekdays, **48h on Sunday** — handled per class.
- **Proactive scheduler:** wakes ~8 s before each window, then retries every
  second until `BOOKED`, `WAITLIST`, or 45 s timeout. Beats sub-10-second
  sellouts.
- **Telegram bot** for status, weekly setup wizard, pause/resume, self-test.
- Works for any Arbox member; the gym, weekly plan, and class filters are
  all in `config.yaml` (or env vars).

> **Running this for your own gym?** Start with
> **[`docs/FORK-SETUP.md`](docs/FORK-SETUP.md)** — a step-by-step walkthrough
> that assumes zero Go background and takes you from "empty machine" to
> "daemon booking your classes" on Mac / Linux / Windows / Raspberry Pi.
> Designed so your friend can clone this and have it working in 20 minutes
> without reading any of the advanced sections below.

> Designed and built with one specific gym in mind, but **everything
> user-specific is parameterised** — see [Multi-user variables](#multi-user-variables).

---

## Table of contents

1. [What it does](#what-it-does)
2. [Quick start (local)](#quick-start-local)
3. [Quick start (Oracle VM — current production)](#quick-start-oracle-vm--current-production)
4. [Updating the code](#updating-the-code)
5. [Configuration](#configuration)
6. [Telegram commands](#telegram-commands)
7. [HTTP API (LLM agents / nanobot)](#http-api-llm-agents--nanobot)
8. [Multi-user variables](#multi-user-variables)
9. [Architecture in 30 seconds](#architecture-in-30-seconds)
10. [Nanobot / MCP integration (same host)](#nanobot--mcp-integration-same-host)
11. [Tuning + rate limits](#tuning--rate-limits)
12. [Security](#security)
13. [Development](#development)

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

> **Forking for your own gym?** Use
> [`docs/FORK-SETUP.md`](docs/FORK-SETUP.md) instead — same info plus
> prerequisites, service-install recipes for Mac / Linux / Windows, and
> troubleshooting. The block below is the fast-path for people who already
> have Go set up.

**Prereqs:** Go **1.24+** (`go version`), an Arbox _member_ account (same
email + password you use on [site.arboxapp.com](https://site.arboxapp.com/)
— not the gym-owner panel), a machine with a clock synced to the internet.

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
./bin/arbox classes list --days 3       # see your gym's real class names
./bin/arbox schedule list --days 7
./bin/arbox schedule resolve --days 7   # online; resolves against live Arbox classes

# 6. Run the daemon
./bin/arbox daemon --interval 1m --lookahead 7
```

The daemon stays in the foreground; press `Ctrl-C` to stop it. To run it
unattended, see the launchd / systemd / Task Scheduler recipes in
[`docs/FORK-SETUP.md § 7`](docs/FORK-SETUP.md#7-keeping-it-running-in-the-background),
or deploy to a VPS via [`docs/DEPLOY-ORACLE.md`](docs/DEPLOY-ORACLE.md).

> **⚠️ The LLM / API / MCP surface is OFF by default and should stay that
> way until you've verified the booker itself works for at least a week.**
> See [HTTP API](#http-api-llm-agents--nanobot) for the opt-in mechanism
> and the security posture.

---

## Quick start (Oracle VM — current production)

The daemon currently runs on an **Oracle Cloud Always-Free Tier VM**
(AMD shape, 2 CPU, 1 GB RAM, 45 GB disk) as a systemd unit, alongside
[nanobot](#nanobot--mcp-integration-same-host). Cost: **$0/month**.

Why Oracle: Arbox's API is fronted by Cloudflare with Bot Fight Mode on,
and Cloudflare scores IP reputation by ASN. Oracle Cloud Frankfurt passes
cleanly; some hosting-heavy ASNs (Fly, parts of Hetzner) get challenged.
Oracle also has a true always-free tier (AMD micro + 4x Ampere A1), so
total cost stays $0/month. Alternatives that also pass Cloudflare and
cost little: Netcup (~€3/mo), Hetzner CAX11 (~€4/mo), or any home
server on a residential IP.

Detailed walkthrough: [`docs/DEPLOY-ORACLE.md`](docs/DEPLOY-ORACLE.md).
The 60-second version:

```bash
# 1. Create Oracle Always-Free VM (Ubuntu 22.04, AMD micro). One-time.
#    Open TCP/22 in the VCN security list.

# 2. On the VM as ubuntu:
mkdir -p ~/arbox/bin ~/arbox/data
# copy config.yaml to ~/arbox/config.yaml
# create ~/arbox/data/.env with:
#   ARBOX_EMAIL=you@example.com
#   ARBOX_PASSWORD=your-arbox-password
#   ARBOX_GYM=CrossFit Downtown
#   TELEGRAM_BOT_TOKEN=123456:ABC...
#   TELEGRAM_CHAT_ID=123456789
chmod 600 ~/arbox/data/.env

# 3. Install the systemd unit (see docs/DEPLOY-ORACLE.md for the full file):
sudo tee /etc/systemd/system/arbox.service >/dev/null <<'UNIT'
[Unit]
Description=arbox-scheduler daemon (auto-book Arbox CrossFit classes)
After=network.target
[Service]
User=ubuntu
WorkingDirectory=/home/ubuntu/arbox
Environment=TZ=Asia/Jerusalem
Environment=ARBOX_ENV_FILE=/home/ubuntu/arbox/data/.env
ExecStart=/home/ubuntu/arbox/bin/arbox daemon --interval=1m --lookahead=7
Restart=on-failure
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload && sudo systemctl enable arbox

# 4. First deploy — from your Mac:
bash scripts/deploy-oracle.sh   # cross-compile, scp, systemctl restart, selftest

# 5. Automate future deploys (optional) — once the Oracle-Deploy GitHub
#    Actions workflow lands (see docs/DEPLOY-ORACLE.md status note),
#    every merge to main auto-deploys. Until then stick with the manual
#    script above; it's fast enough (~10 s) for day-to-day use.
```

After this, control the app entirely from Telegram (see next section).

---

## Updating the code

Day-to-day loop when you change the Go source:

```bash
# 1. Branch off main (main is what Oracle runs)
git checkout main && git pull
git checkout -b feat/<short-name>

# 2. Edit + test locally
$EDITOR ...
go test ./...                              # must be green
go build -o bin/arbox ./cmd/arbox           # optional: catch build errors

# 3. Commit (per-repo GPG is already configured → commits show "Verified")
git commit -am "feat: <one-line summary>"

# 4. Push + open PR
git push -u origin HEAD
gh pr create --fill

# 5. Merge the PR (branch protection requires a PR; 0 reviews needed; squash).

# 6. Deploy to Oracle. Two ways, same sequence:
#    a) Manual (available now):
#         bash scripts/deploy-oracle.sh
#       Builds linux/amd64, scps to Oracle, systemctl restart, selftest.
#    b) Automated (once the Oracle-Deploy workflow lands):
#         GitHub Actions cross-compiles, scps, restarts, selftests on every
#         merge to main. Telegram "🟢 Online" pings when the new rev boots.
```

Rollback: either `git revert <sha> && git push` (CI redeploys, ~1 min),
or on your Mac check out a known-good SHA and `bash scripts/deploy-oracle.sh`
(~10 s). State files on `~/arbox/data/` are never touched by a deploy.

---

## Configuration

Two files matter:

- **`config.yaml`** — checked into the repo. Edit it once when you set up.
- **`.env`** — secrets. Gitignored. On Oracle it lives at `~/arbox/data/.env` (path controlled by `ARBOX_ENV_FILE`; auto-created on first login).

### `config.yaml` — minimum viable

```yaml
timezone: Asia/Jerusalem      # or your gym's TZ
default_time: "09:00"

# REQUIRED if your Arbox account belongs to >1 gym; substring of the box or
# location name. Can also be set via ARBOX_GYM env var.
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
to `user_plan.yaml` in the data dir; the daemon merges it on every tick —
no restart needed.

---

## Telegram commands

`setMyCommands` registers all of these in the in-Telegram `/` menu on boot:

| Command | What it does |
|---|---|
| `/start`, `/help` | One-shot intro |
| `/status` | Saved selections per weekday + your real Arbox bookings (shows `WAITLIST N/M` when you're waitlisted) |
| `/morning [HH-HH] [days\|week]` | Live class list, default 06–12 next 1 day, with waitlist position |
| `/evening [HH-HH] [days\|week]` | Live class list, default 16–22 next 1 day, with waitlist position |
| `/setup` | Inline buttons: pick weekly slots from real Arbox classes. Each day has two save buttons: **💾 Every {Sunday}** (persists to `user_plan.yaml`, every future week) and **📅 Just {Sun 26 Apr}** (one-time override for just that date). |
| `/setupdone` | Bulk-save every day's selections to `user_plan.yaml` (alternative to per-day save buttons) |
| `/setupcancel` | Discard the in-progress `/setup` session |
| `/pause [Nh\|Nd\|until DATE [HH:MM]] [reason]` | Stop auto-booking (default 24h) |
| `/resume` | Resume auto-booking |
| `/version` | Build rev + gym + TZ + locations_box_id + pause state |
| `/selftest` | 8 health checks + next 3 scheduled bookings |

Notifications you'll receive automatically:

- `🟢 *Online*` — daemon boot
- `🫀 *Heartbeat*` — once per local calendar day with self-test + upcoming bookings
- `✅ *Booked*` / `⏳ *Waitlisted*` / `❌ *Booking failed*` — every booking attempt
- `🔴 *Shutting down*` — on SIGTERM (systemd restart, container stop, etc.)
- `⚠️ *Daemon error*` — for tick or booker errors

Only the chat id matching `TELEGRAM_CHAT_ID` is allowed to send commands.

---

## HTTP API (LLM agents / nanobot)

> **🚨 Off by default. Keep it that way until you're ready.**
>
> The HTTP server **refuses to start** unless `ARBOX_API_READ_TOKEN` or
> `ARBOX_API_ADMIN_TOKEN` is set. Leave both env vars unset and you get
> zero LLM surface, zero attack surface — the daemon keeps doing its
> booking job with no extra network exposure.
>
> **Don't enable the API until all of these are true:**
> 1. The plain booker has been booking your classes correctly for at
>    least a week, including at least one successful window strike.
> 2. You understand that an LLM with the **admin** token can, in principle,
>    book / cancel / pause anything — the `?confirm=1` gate helps but is
>    not a substitute for trusting the client you wire up.
> 3. You've read [`docs/API.md`](docs/API.md) and
>    [`docs/NANOBOT.md`](docs/NANOBOT.md) end-to-end.
>
> There is no reason to enable this just to use the scheduler — Telegram
> gives you the same status + setup interface without any of the LLM risk.

The daemon also serves a small REST surface at `/api/v1/` for nanobot /
Claude / OpenAI tool-calling. It runs in the same process as everything
else; no extra service to operate.

- **Loopback-only default** (`127.0.0.1:8080`) — both arbox and nanobot
  run as the same user on the same Oracle VM, so there's no network
  exposure. Override with `ARBOX_HTTP_ADDR=:8080` only if you need to
  reach it from outside the host.
- **Two-tier bearer auth**: `ARBOX_API_READ_TOKEN` (GETs only) and
  `ARBOX_API_ADMIN_TOKEN` (everything).
- **Mutations require `?confirm=1`** on the URL on top of the admin
  token (belt + suspenders — an LLM has to consciously opt in).
- **60 req/min per token, burst 30**. Audit log at
  `~/arbox/data/audit.jsonl` on Oracle records every mutation (override
  path via `ARBOX_AUDIT_LOG`).
- **OpenAPI 3.1 spec** at `/api/v1/openapi.json` — nanobot discovers
  tools from it automatically.
- **Disabled** (server doesn't start) if both API tokens are unset — so
  enabling is deliberate.

See:

- [docs/API.md](docs/API.md) — endpoint reference + curl examples.
- [docs/NANOBOT.md](docs/NANOBOT.md) — exact nanobot config snippet for
  the same-VM loopback setup.

### Enable it on Oracle

```bash
ssh oracle-vps 'cat >> ~/arbox/data/.env' <<EOF
ARBOX_API_READ_TOKEN=$(openssl rand -hex 32)
ARBOX_API_ADMIN_TOKEN=$(openssl rand -hex 32)
EOF
ssh oracle-vps 'sudo systemctl restart arbox'
ssh oracle-vps 'curl -s http://127.0.0.1:8080/api/v1/healthz'   # → {"ok":true, ...}
```

---

## Multi-user variables

Everything user/gym-specific is one of:

| Where | Field | What it controls | How to override |
|---|---|---|---|
| `config.yaml` | `timezone` | Gym wall clock | `ARBOX_TIMEZONE` env |
| `config.yaml` | `gym` | Pick the right gym when account has many | `ARBOX_GYM` env |
| `config.yaml` | `category_filter.include` / `exclude` | Substring filter (Hebrew/English/etc.) | edit YAML |
| `config.yaml` | `days` / `default_time` | Weekly plan | edit YAML or use `/setup` → `user_plan.yaml` |
| `.env` | `ARBOX_EMAIL`, `ARBOX_PASSWORD` | Member login | env vars / host secrets |
| `.env` | `ARBOX_TOKEN`, `ARBOX_REFRESH_TOKEN` | Auto-managed | – |
| `.env` | `ARBOX_BOX_ID`, `ARBOX_LOCATIONS_BOX_ID` | Discovered (re-discovered if `gym:` is set) | – |
| `.env` | `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Telegram control | `~/arbox/data/.env` on Oracle |
| systemd unit | `Environment=TZ=...` | Process timezone | leave default; `ARBOX_TIMEZONE` in `.env` overrides config |
| systemd unit | `ExecStart=... --interval=1m --lookahead=7` | Daemon tick rate / lookahead | edit `/etc/systemd/system/arbox.service` |

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
│  Oracle Cloud Free VM (ubuntu@152.x.x.x, AMD micro, systemd)         │
│                                                                      │
│  ├─ arbox.service    — the daemon (this repo)                        │
│  │  ┌─ Telegram bot (long-poll) ──► /status /morning /setup /pause   │
│  │  ├─ 1-min heartbeat ticker   ──► alive log + safety-net booker    │
│  │  └─ Proactive scheduler      ──► sleep until next WindowOpen,     │
│  │                                   burst BookClass every 1s × 45s  │
│  │                                                                   │
│  │  ┌─ ~/arbox/data/.env               — tokens (auto-renewed on 401)│
│  │  ├─ ~/arbox/data/user_plan.yaml     — overlay from /setup         │
│  │  ├─ ~/arbox/data/pause.json         — /pause state                │
│  │  └─ ~/arbox/data/booking_attempts.json — terminal results, dedup  │
│  │                                                                   │
│  └─ nanobot.service  — LLM gateway (MCP host, not in this repo)      │
│     └── registers arbox as an MCP server (see integration section)   │
└──────────────────────────────────────────────────────────────────────┘

           Arbox member API  apiappv2.arboxapp.com
           (login, schedule/betweenDates, scheduleUser/insert,
            standBy/insert, boxes/locations, boxes/<id>/memberships/1)
```

No public HTTP port. Outbound only (except SSH/22 for ops + deploy).
State lives in `~/arbox/data/` on the VM's 45 GB disk.

---

## Booking timeline (when does `BookClass` actually fire?)

The proactive scheduler does **not** fire 10 s before the window — that
would just earn a *"registration not yet open"* error. It fires **~250 ms
before** `WindowOpen` so the request lands at the Arbox server right at, or
just after, the window opens.

For a window of `T = 08:00:00.000` (Israel time):

| Clock | Action | Constant |
|---|---|---|
| `T − 8 s` | Wake; reload config; check `/pause`; **auth probe** (1 GET) so a stale token re-logins now, not at the strike | `proactiveLead = 8s` |
| `T − 250 ms` | `STRIKE` log line; **first `BookClass` POST** sent | `proactiveStrikeOffset = 250ms` |
| `T + ~0–600 ms` | API receives the POST right around `T` (the offset compensates for clock skew + network RTT) | – |
| every `+1 s` after | Next attempt: `BookClass` for the next priority tier, or `JoinWaitlist` if class is full | `burstInterval = 1s` |
| stop | First `BOOKED`/`WAITLIST` **or** `T + 45 s` **or** `ClassStart − 1 s` | `burstDuration = 45s` |

Why **not** fire 10 s early: an early POST returns *"registration not yet
open"* and burns a retry slot for nothing. **250 ms** is the smallest
offset that's:

- big enough to cover **clock skew** (VM clock vs Arbox server clock),
- big enough to cover **network RTT** (~30–120 ms from most EU clouds to Israel),
- small enough that the API server-side timestamp lands **at or just after** `WindowOpen`.

If you ever see `❌ Booking failed` with a *"not open"* message, **raise**
`proactiveStrikeOffset` (e.g. 350 ms). If your gym sells out so fast that
even 250 ms loses, **lower** it (e.g. 100 ms). Both live in
`cmd/arbox/proactive.go`; we can also wire them as `daemon` flags so you
can tune without redeploys.

---

## Nanobot / MCP integration (same host)

The VM also runs **nanobot** (`/usr/local/bin/nanobot gateway`, systemd
unit `nanobot.service`, working dir `/home/ubuntu/.nanobot/`). Because
both services run as `ubuntu` on the same host, arbox exposes a
**loopback-only HTTP API** that nanobot consumes as an MCP server — no
ports exposed to the public internet, zero network RTT, single trust
boundary.

See the detailed endpoint reference in [docs/API.md](docs/API.md) and
the nanobot wiring walkthrough (with the exact JSON snippet) in
[docs/NANOBOT.md](docs/NANOBOT.md). The [HTTP API section above](#http-api-llm-agents--nanobot)
has the one-command enable-it-on-Oracle recipe.

Design properties:

- **Loopback by default.** `ARBOX_HTTP_ADDR` defaults to
  `127.0.0.1:8080`; listens on all interfaces only if you explicitly
  override.
- **Bearer auth.** `ARBOX_API_READ_TOKEN` (GETs) and
  `ARBOX_API_ADMIN_TOKEN` (mutations). Server refuses to start if both
  are unset — enabling is deliberate.
- **`?confirm=1` for every mutation.** An LLM must consciously opt in;
  first call always returns a dry-run preview.
- **Same `bookerMu` as Telegram + proactive scheduler.** `POST /book`
  takes the same lock the daemon's booking-window burst uses, so you
  can't fire `BookClass` for the same `schedule_id` twice. The
  `booking_attempts.json` file also deduplicates across restarts.
- **60 req/min per token, burst 30.** `X-RateLimit-*` + `Retry-After`
  on 429.
- **Audit log** of every mutation at `~/arbox/data/audit.jsonl`
  (`client_ip`, token kind, endpoint, confirm flag, response). Readable
  via `GET /api/v1/audit`.

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
- On Oracle, secrets live in `~/arbox/data/.env` owned by `ubuntu:ubuntu`
  mode `600`. Nothing sensitive is in the binary or the repo.
- Deploys use a **dedicated ed25519 key** (`~/.ssh/arbox-deploy-ci`) whose
  pubkey is the only CI-authorised entry in `authorized_keys`; the
  private half is only in the `ORACLE_SSH_KEY` GitHub repo secret.
- The Telegram bot ignores any chat id other than `TELEGRAM_CHAT_ID`.
- The booking machinery is gated by `bookerMu` so the proactive goroutine
  and the safety-net ticker can never fire `BookClass` for the same
  `schedule_id` simultaneously, and `booking_attempts.json` makes terminal
  outcomes survive process restarts.
- The repo never contains personal gym names, phone numbers, or chat ids;
  use `ARBOX_GYM` and your host's secret store for that data.

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
cmd/arbox/                       # cobra CLI + Telegram bot + daemon
internal/arboxapi/               # member API client (login, schedule, book, waitlist)
internal/config/                 # YAML load + validate + env overrides
internal/schedule/               # window math (24h / Sunday-48h, DST-correct)
internal/notify/                 # Telegram + stdout notifiers
scripts/deploy-oracle.sh         # manual deploy Mac → Oracle VM
.github/workflows/oracle-deploy.yml  # CI: test + auto-deploy on merge to main
docs/DEPLOY-ORACLE.md            # detailed Oracle walkthrough (production)
docs/FORK-SETUP.md               # "I want to run this for my own gym" guide
```

PRs welcome. Tests must pass and stay deterministic (no fixed-date assumptions).
