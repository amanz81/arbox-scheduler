# Deploying arbox-scheduler to Fly.io

> **⚠️ Legacy / cold-standby only. Current production target is Oracle
> Cloud Free Tier — see [`DEPLOY-ORACLE.md`](DEPLOY-ORACLE.md).**
>
> This guide is kept because the Fly app (`arbox-scheduler.fly.dev`,
> machine `185947ea234548` in `fra`, volume `vol_vz8kqqjlyo97n5jv`) is
> still provisioned in the **stopped** state as a rollback target, and
> the instructions below are accurate for re-creating it from scratch.
>
> **Known issue:** Cloudflare's bot-fight-mode rules blocked
> `apiappv2.arboxapp.com` from Fly's Frankfurt ASN starting April 2026,
> so a Fly-hosted daemon currently gets HTTP 403 on every Arbox call.
> Even with realistic browser headers (merged in PR #3), Frankfurt IPs
> fail the TLS JA3 + reputation score. Migrating back is only viable if
> either (a) Fly moves the app to a different ASN, (b) Cloudflare
> loosens the rule, or (c) we proxy through a residential IP.

This is the one-shot walkthrough for getting the daemon running on Fly.io
with a persistent volume (so tokens survive restarts).

Total cost: **$0/month** within the Fly Hobby plan — one shared-cpu-1x VM
(256 MB) + 1 GB volume fit well under the free allowance, and the app is
outbound-only (no public ports, no bandwidth worth mentioning).

---

## 0. Prerequisites

```bash
# Install flyctl (macOS)
brew install flyctl

# Sign up / log in (opens a browser)
fly auth signup    # or `fly auth login` if you already have an account
```

You'll be asked for a credit card during signup. Fly uses it to
rate-limit abuse; you won't be charged as long as you stay on Hobby.

---

## 1. Launch the app (one-time)

From the repo root:

```bash
fly launch --no-deploy --copy-config --name arbox-scheduler --region fra
```

- `--no-deploy`  — don't deploy yet; we need to set secrets + create the volume first.
- `--copy-config` — reuse `fly.toml` from the repo instead of regenerating it.
- `--name`       — app name. If `arbox-scheduler` is taken, pick another (e.g. `arbox-<yourhandle>`).
- `--region fra` — Frankfurt, the lowest-latency free region for Israel.

Fly will create the app and update `fly.toml` in place.

---

## 2. Create the persistent volume

Tokens are refreshed on every login; they must survive redeploys.

```bash
fly volumes create arbox_data --region fra --size 1
```

The name (`arbox_data`) must match `[mounts].source` in `fly.toml`.

---

## 3. Set secrets (Arbox credentials + optional gym disambiguator)

These are encrypted at rest by Fly and never end up in the image.

```bash
fly secrets set \
  ARBOX_EMAIL='you@example.com' \
  ARBOX_PASSWORD='your-arbox-password'
```

If your Arbox account belongs to **more than one gym**, also set the gym
substring so the daemon picks the right box+location every time
(otherwise discovery picks the first one returned by the API and you may
end up booking for the wrong gym):

```bash
fly secrets set ARBOX_GYM='CrossFit Downtown'
```

`ARBOX_GYM` overrides `gym:` in `config.yaml` and forces re-discovery on
every call, so a stale `ARBOX_LOCATIONS_BOX_ID` is auto-corrected.

On first boot the daemon will call `/api/v2/user/login` using them,
discover your box + locations, and write the access/refresh tokens to
`/data/.env`. From then on only the refresh token is used to stay logged in.

---

## 3b. Telegram (optional — outbound notifications only)

The daemon does **not** run a Telegram webhook server. `/start` in the bot
does nothing until **you** tell Fly your numeric `chat_id` and the bot
token — then the daemon calls `sendMessage` **to you** (MarkdownV2).

### Get your `chat_id`

1. Put the bot token from @BotFather in your local `.env` (never commit it):

   ```bash
   TELEGRAM_BOT_TOKEN=123456:ABC...
   ```

2. Open your bot in Telegram and tap **Start** (or send any message).

3. From the repo root:

   ```bash
   go run ./cmd/arbox telegram discover
   ```

   It prints a line like `chat_id=123456789 ...`

4. Add **both** secrets on Fly (web UI → your app → **Secrets**, or `fly secrets set`):

   - `TELEGRAM_BOT_TOKEN` — same value as in `.env`
   - `TELEGRAM_CHAT_ID` — the integer from step 3

5. Push a new deploy (or wait for the next GitHub Actions run after `git push`).
   On boot you should get one **online** message with version + first tick
   summary; after that, at most one **heartbeat** per local calendar day.

6. **Slash commands:** the daemon calls Telegram `setMyCommands` on boot.
   The `/` menu includes:

   - **`/status`** — saved selections per weekday + your real Arbox bookings.
   - **`/morning [HH-HH] [days|week]`** — live class list (default 06–12, 1 day).
   - **`/evening [HH-HH] [days|week]`** — live class list (default 16–22, 1 day).
   - **`/setup`** — fetches the **real** class list from Arbox for the next
     occurrence of each weekday and sends **inline buttons** (✓/○) so you can
     toggle which slots belong in your weekly plan (first tapped = highest
     priority). **`/setupdone`** writes the result to
     `user_plan.yaml` on the Fly volume (default: `/data/user_plan.yaml`) and
     the daemon **reloads it every tick** — no restart needed.
     **`/setupcancel`** — discards the in-progress session.
   - **`/pause [Nh|Nd|until DATE [HH:MM]] [reason]`** — stop auto-booking
     (default 24 h, max 90 d). State is persisted to `/data/pause.json`.
   - **`/resume`** — clear the pause.
   - **`/version`** — show the deployed git rev, gym, locations_box_id, TZ,
     pause state. Use this to confirm a redeploy actually shipped.
   - **`/selftest`** — 8 health checks (auth, gym binding, membership,
     schedule fetch, attempts file, plan resolution, schedule cache) plus
     the next 3 scheduled bookings.

   Optional env vars (defaults work on Fly with `ARBOX_ENV_FILE=/data/.env`):

   - `ARBOX_USER_PLAN` — path to the merged `days:` overlay (default
     `/data/user_plan.yaml` next to your `.env` directory).
   - `ARBOX_SETUP_SESSION` — JSON session for `/setup` (default
     `/data/setup_session.json`).

   You may see an extra **shutdown** line on each deploy — that is the old
   machine stopping before the new one starts.

If you ever pasted the bot token in a chat, rotate it in @BotFather with
`/revoke` and update the Fly secret.

---

## 4. Deploy

```bash
fly deploy
```

Fly will:
1. Upload the repo (respecting `.dockerignore`) to a remote builder.
2. Build the image (takes ~1-2 min first time, ~20 s on rebuilds).
3. Boot one machine with the volume mounted at `/data`.

---

## 5. Verify

Tail logs:

```bash
fly logs
```

You should see output like:

```
[daemon] version=dev interval=1m0s lookahead=7d tz=Asia/Jerusalem
[tick] 2026-04-17 17:04:01 IDT  locations_box_id=1234
[proactive] next strike at 2026-04-18 08:00:00.000 IDT (in 14h55m59s)
```

Open a shell on the machine:

```bash
fly ssh console
# inside:
/app/arbox selftest
/app/arbox me
/app/arbox schedule resolve --days 7
cat /data/.env   # tokens are here, owned by the `arbox` user
```

Or simply send **`/selftest`** in Telegram — same content, no SSH needed.

---

## Day-2 operations

| Task                             | Command                                    |
| -------------------------------- | ------------------------------------------ |
| Push new code                    | `fly deploy`                               |
| Restart the daemon               | `fly machine restart`                      |
| Tail logs                        | `fly logs`                                 |
| Change a secret                  | `fly secrets set ARBOX_PASSWORD=...`       |
| Switch gym (multi-gym account)   | `fly secrets set ARBOX_GYM='New Gym Name'` |
| Pause auto-booking (per-day)     | `/pause 3d` in Telegram                    |
| Pause via infra (immediate)      | `fly scale count 0`                        |
| Resume                           | `/resume` in Telegram, or `fly scale count 1` |
| Shell in                         | `fly ssh console`                          |
| Run health check                 | `/selftest` in Telegram, or `fly ssh console -C '/app/arbox selftest'` |
| Verify deployed build            | `/version` in Telegram                     |
| Destroy everything               | `fly apps destroy <your-app-name>`         |

---

## Troubleshooting

**`fly deploy` fails with "no matching volume"**
The volume name or region in `fly.toml` doesn't match what you created.
Check `fly volumes list`.

**Daemon logs `auth-probe fetch failed: ... status 401`**
Your Arbox password changed or the refresh token expired. Reset secrets:

```bash
fly secrets set ARBOX_EMAIL='...' ARBOX_PASSWORD='...'
fly ssh console -C 'rm /data/.env'
fly machine restart
```

**Booking goes to the wrong gym**
Your Arbox account belongs to multiple gyms and discovery picked the first
one. Set the gym substring so re-discovery picks the right box+location:

```bash
fly secrets set ARBOX_GYM='CrossFit Downtown'
fly ssh console -C 'sed -i /^ARBOX_BOX_ID=/d -e /^ARBOX_LOCATIONS_BOX_ID=/d /data/.env'
fly machine restart
```

Then `/selftest` in Telegram — the **Locations API** check should report
your gym name and the new `locations_box_id`.

**`/version` shows an older git rev than what's on `main`**
The Fly deploy didn't pick up the latest push — re-run `fly deploy` (or
re-trigger the GitHub Actions workflow if you've wired one).

**"Out of memory" when building**
Shouldn't happen with 256 MB for this tiny binary, but if it does, bump
memory in `fly.toml`:

```toml
[[vm]]
  size   = "shared-cpu-1x"
  memory = "512mb"
```

Then `fly deploy` again.
