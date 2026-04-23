# Running arbox-scheduler for your own gym

> New here? This is the walkthrough for someone who wants to clone this repo
> and run it for **their own** Arbox member account at **their own** gym.
> No prior knowledge of Go, systemd, or any of the "advanced" sections
> (LLM API, MCP, nanobot, OpenClaw) is required. You never need to touch
> those until you want to.

---

## 0. What you'll end up with

A small program that runs on your laptop (or a home PC, or a Raspberry Pi,
or a VPS — your choice) and, at the exact moment your gym opens its
booking window, tries to book the class you told it you want. Hit-rate is
usually >95% even when classes sell out in 10 seconds.

If it works and you want Telegram notifications you can add that later.
If it doesn't work you can just `Ctrl-C` it — it never touches Arbox
except to log in and book classes you asked for.

---

## 1. Check you have what you need

### 1a. An Arbox member account

You log in to [site.arboxapp.com](https://site.arboxapp.com/) or the
Arbox app on your phone, you see your weekly classes, you book classes
manually. That's the account you need. Your gym has to be a real Arbox
customer — if you don't know, check with your gym's front desk.

**You must NOT use** the gym-owner / admin panel credentials. Those are
on a different backend and this scheduler won't work with them.

### 1b. Go 1.24 or newer

The program is written in Go. You need to install the Go compiler once,
then `go build` turns the source into a single binary.

- **macOS:** `brew install go` (if you use Homebrew) or download the
  installer from <https://go.dev/dl/>.
- **Linux (Ubuntu/Debian):**
  ```bash
  sudo apt update && sudo apt install -y golang-1.24
  echo 'export PATH=/usr/lib/go-1.24/bin:$PATH' >> ~/.bashrc && source ~/.bashrc
  ```
- **Windows:** Download the .msi installer from <https://go.dev/dl/> and
  run it. Then open a fresh PowerShell window.

Check it worked:

```bash
go version
# → go version go1.24.x ...
```

### 1c. ~50 MB of disk and a clock that's synced to the internet

The scheduler fires at the exact second a booking window opens. If your
machine's clock drifts by more than a few seconds you'll miss the strike.
Your OS almost certainly syncs to NTP automatically; you don't need to do
anything unless you've explicitly disabled it.

---

## 2. Clone (or fork) the repo

```bash
git clone https://github.com/amanz81/arbox-scheduler.git
cd arbox-scheduler
```

**If you want to keep your own copy + commit your weekly plan changes:**
fork the repo on GitHub first (click "Fork" on the repo page), then
clone *your* fork instead. That way:

- Your `config.yaml` edits stay on your fork.
- You can pull upstream updates with `git remote add upstream <original-url> && git pull upstream main`.
- You never accidentally open a pull request with your personal schedule.

Your `.env` (with credentials) is gitignored so it never gets committed
either way — you can see the rule in `.gitignore`.

---

## 3. Configure for your gym

### 3a. Credentials → `.env`

```bash
cp .env.example .env
```

Open `.env` in your editor and set:

```
ARBOX_EMAIL=you@example.com
ARBOX_PASSWORD=your-arbox-password
```

Leave every other variable as a comment for now. `ARBOX_TOKEN` and
`ARBOX_REFRESH_TOKEN` will be filled in automatically by the login step.

**`.env` never leaves your machine.** Double-check `git status` — you
should NOT see `.env` in the "Changes to be committed" list. If you do,
something is wrong with `.gitignore`; stop and ask.

### 3b. Weekly plan → `config.yaml`

Open `config.yaml` in your editor. It already has a working plan (it's
the author's schedule — different days, times, gym halls). You need to
rewrite the `days:` block for your own schedule.

Minimal example for a gym that has class halls named "Studio 1" and
"Studio 2":

```yaml
timezone: America/New_York          # or whatever your gym uses
default_time: "09:00"

# Optional: only required if your Arbox account has >1 gym attached.
# Case-insensitive substring of your gym's name.
# gym: "CrossFit Downtown"

# Global filter so Kids / Teens / Open-gym slots never get booked by
# accident. Uses substring match against the class title. Edit for your
# gym's naming.
category_filter:
  include:
    - "CrossFit"
    - "WOD"
  exclude:
    - "Kids"
    - "Teens"
    - "Open Box"

days:
  monday:
    enabled: true
    time: "18:00"
    category: "CrossFit"
  tuesday:
    enabled: true
    time: "18:00"
    category: "CrossFit"
  wednesday:
    enabled: false                  # rest day

  # Priority example: prefer Studio 1 at 09:00, fall back to Studio 2.
  thursday:
    enabled: true
    options:
      - { time: "09:00", category: "Studio 1" }
      - { time: "09:00", category: "Studio 2" }

  friday: { enabled: false }
  saturday: { enabled: true, time: "08:00", category: "Open Gym" }
  sunday: { enabled: false }
```

**Don't worry about getting the plan perfect up front.** You can always
update it later (see "Telegram setup wizard" below).

### 3c. Figure out your gym's class category names

Quickest way: after the build + login step (section 4), run:

```bash
./bin/arbox classes list --days 7
```

That prints every scheduled class for the next 7 days with its exact
`CATEGORY` string. Copy a substring of whichever class names you want
into your `config.yaml`.

---

## 4. Build + first login

```bash
# Build the binary (takes ~10 seconds)
go build -o bin/arbox ./cmd/arbox

# First-time login. Pulls a JWT access token + auto-discovers your gym.
# Writes the results into .env so you don't have to log in again for a while.
./bin/arbox auth login

# Health check — make sure the config + credentials + gym discovery all work
./bin/arbox selftest

# See what classes your gym actually has right now
./bin/arbox classes list --days 3

# See what the daemon WOULD book if it ran today (no network-side effects)
./bin/arbox schedule resolve --days 7
```

If any of these fail with "gym not found" or similar, tweak the `gym:`
field in `config.yaml` or set `ARBOX_GYM=...` in `.env` with a unique
substring of your gym name.

**At this point nothing has been booked.** The `login` + `classes list` +
`schedule resolve` commands are all read-only.

---

## 5. Run the daemon for real

```bash
./bin/arbox daemon --interval 1m --lookahead 7
```

Leave the terminal open. The daemon:

- Ticks every minute, logs what it sees
- Wakes ~8 seconds before your next booking window
- Fires `BookClass` right at the window-open instant
- Falls back to waitlist if the class is full
- Logs terminal outcomes to `./booking_attempts.json` so it doesn't retry
  things it already succeeded/failed on

Press `Ctrl-C` to stop. Nothing is left running in the background unless
you set that up yourself (section 7).

---

## 6. How to tell if it's working

Best test: the next morning after your first window opens, check the
Arbox app. You should either be:

- **Booked** into the class you asked for (ideal), or
- **On the waitlist** (class sold out before the scheduler got there, or
  was full at window open)

The daemon prints a compact log line for every booking attempt. Example:

```
[proactive] T-8s 2026-04-24 08:30 CrossFit Hall A  (window opens in 8s)
[STRIKE] 08:30:00.251 → BookClass POST id=53268139
[booker] 2026-04-24 08:30 CrossFit Hall A — BOOKED (id 53268139)
```

If you see `book FAILED … http 514 medicalWavierRestricted`, Arbox wants
you to sign a waiver form in the Arbox app before you can book that
class. Sign it in the app, then restart the daemon.

---

## 7. Keeping it running in the background

### 7a. macOS (launchd)

Create `~/Library/LaunchAgents/com.you.arbox.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
 "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>com.you.arbox</string>
    <key>WorkingDirectory</key><string>/Users/YOU/arbox-scheduler</string>
    <key>ProgramArguments</key>
    <array>
      <string>/Users/YOU/arbox-scheduler/bin/arbox</string>
      <string>daemon</string>
      <string>--interval</string><string>1m</string>
      <string>--lookahead</string><string>7</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>/tmp/arbox.log</string>
    <key>StandardErrorPath</key><string>/tmp/arbox.err</string>
  </dict>
</plist>
```

Load it:

```bash
launchctl load ~/Library/LaunchAgents/com.you.arbox.plist
tail -f /tmp/arbox.log
```

Unload with `launchctl unload ...` (same path). Your Mac must be awake or
at least Power Nap-enabled near your booking windows.

### 7b. Linux (systemd)

```ini
# /etc/systemd/system/arbox.service
[Unit]
Description=arbox-scheduler
After=network-online.target
[Service]
User=YOU
WorkingDirectory=/home/YOU/arbox-scheduler
ExecStart=/home/YOU/arbox-scheduler/bin/arbox daemon --interval=1m --lookahead=7
Restart=on-failure
Environment=TZ=America/New_York
[Install]
WantedBy=multi-user.target
```

Then:

```bash
sudo systemctl enable --now arbox
sudo systemctl status arbox
sudo journalctl -u arbox -f
```

### 7c. Windows (Task Scheduler)

1. Open Task Scheduler, Create Task.
2. General: name `arbox`, "Run whether user is logged on or not".
3. Triggers: At startup.
4. Actions: Program/script = `C:\path\to\arbox-scheduler\bin\arbox.exe`,
   arguments = `daemon --interval 1m --lookahead 7`,
   "Start in" = `C:\path\to\arbox-scheduler`.
5. Settings: "If the task fails, restart every 1 minute, up to 3 times".

### 7d. Raspberry Pi / always-on Linux box

Same as 7b. A Pi is honestly the best host — low power, always on,
dedicated. Build the binary for ARM with `GOOS=linux GOARCH=arm64 go build ...`
on your dev machine, scp to the Pi.

---

## 8. Optional: Telegram bot

Totally optional; skip this whole section if you don't want a chat
interface. The scheduler runs perfectly fine without Telegram.

If you want status/commands from your phone:

1. Create a bot: DM [@BotFather](https://t.me/BotFather) on Telegram, run
   `/newbot`, follow prompts, save the token it gives you.
2. DM your new bot once to start the conversation.
3. In your `.env`:
   ```
   TELEGRAM_BOT_TOKEN=<the token BotFather gave you>
   ```
4. Discover your chat id:
   ```bash
   ./bin/arbox telegram discover
   ```
   It prints your numeric chat id. Add it to `.env`:
   ```
   TELEGRAM_CHAT_ID=<the number>
   ```
5. Restart the daemon. You should now receive booking notifications.
6. In the Telegram chat, try `/start`, `/help`, `/status`, `/selftest`.
   Full command list is in the main [README](../README.md#telegram-commands).

---

## 9. ⚠️ LLM / API / MCP features — do NOT enable yet

The repo includes an optional HTTP REST API (`/api/v1/...`) that lets
LLM agents (nanobot, Claude Code, ChatGPT Actions, etc.) book/cancel/pause
your classes automatically. **It is OFF by default** and you should keep
it off until:

1. Your plain daemon + Telegram setup has been booking classes correctly
   for at least a week.
2. You understand the two-tier bearer token + `?confirm=1` mutation gate
   (see [`docs/API.md`](API.md)).
3. You've read [`docs/NANOBOT.md`](NANOBOT.md) if you plan to bridge to an
   MCP client, and you understand the risk: a prompt injection against
   your LLM agent can, in principle, cause it to book/cancel/pause
   anything the admin token can touch.

The server literally won't start without `ARBOX_API_READ_TOKEN` and/or
`ARBOX_API_ADMIN_TOKEN` set. Leave those env vars unset and you get zero
LLM surface, zero attack surface. The only thing that stays on is the
booker itself, which is what you actually came here for.

---

## 10. Common problems

| Symptom | Most likely cause | Fix |
|---|---|---|
| `auth login` fails with "401" | Wrong email or password, or you used the gym-owner panel creds by mistake | Use your member credentials from [site.arboxapp.com](https://site.arboxapp.com/) |
| `selftest` says "no classes for today" | Timezone mismatch | Set `timezone:` in `config.yaml` to your gym's zone |
| Daemon logs "no match" for every day | `category_filter.exclude` is swallowing your target class because its title contains an excluded substring | Remove the excluded word from the list, or narrow `include` to something more specific |
| `http 514 medicalWavierRestricted` on every attempt | Your gym added a medical-waiver requirement | Sign the waiver in the Arbox mobile/web app, then restart the daemon |
| Booking happens but gets cancelled immediately | You manually cancelled it in the app OR the gym enforces a late-cancel policy that auto-refunds a credit | Check Arbox app for cancellation reason |
| `http 403` with HTML challenge page in response | Your host's IP is being rate-limited or blocked by Cloudflare (rare, mostly seen on cheap VPS ASNs) | Move to a different host (home IP, Oracle Free Tier, Netcup, Hetzner) or proxy through a residential IP |

---

## 11. What to do next

Once the daemon has been booking your classes reliably for a week:

- Read the main [README](../README.md) — architecture, tuning, rate limits.
- If you want to deploy to a VPS instead of running locally, see
  [`docs/DEPLOY-ORACLE.md`](DEPLOY-ORACLE.md) for the Oracle Cloud Free
  Tier recipe (same $0/month deployment the author uses).
- Only **then** consider the optional LLM API if that workflow interests
  you.

Questions, bugs, missing features — open an issue or a PR on the repo.
