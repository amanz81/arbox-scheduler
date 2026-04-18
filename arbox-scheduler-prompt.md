# Arbox Auto-Scheduler — Build Prompt

Copy-paste the prompt below into Claude Code on your other computer to bootstrap the project.

---

## Prompt

I want to build a CLI app in **Go** that automatically books my weekly CrossFit classes on **Arbox** as soon as the booking window opens. We'll build and test locally first, then deploy to my personal Linux VPS (which already runs another service of mine).

### Background & constraints

- **Arbox booking windows:**
  - Most days: booking opens **24 hours** before class start time.
  - **Sunday:** booking opens **48 hours** before class start time.
- **Arbox API:** I don't know of public docs. We'll likely reverse-engineer it from the web app (DevTools → Network tab while booking manually, capture auth + booking requests as cURL).
- **Timezone:** Asia/Jerusalem (Israel). Classes and windows should all be computed in local time.

### My weekly schedule preferences

| Day       | Class time | Notes                                                 |
|-----------|-----------:|-------------------------------------------------------|
| Sunday    |    8:00 AM | Earlier so I can head to the office after             |
| Monday    |    8:30 AM | One of the only 8:30 slots offered                    |
| Tuesday   |    9:00 AM | Default preferred time                                |
| Wednesday |          — | Rest day (no booking)                                 |
| Thursday  |    8:30 AM | One of the only 8:30 slots offered                    |
| Friday    |   11:00 AM | Weekend slot                                          |
| Saturday  |          — | Rest day (no booking)                                 |

Default preferred time is **9:00 AM** unless overridden per day as above.

### Behavior

- **When the booking window opens**, the app should fire the booking request immediately — firing on time is the whole point, because popular slots fill up fast.
- **If the class is full**, auto-join the **waiting list** and send me a **notification**. Do NOT build a fallback-class mechanism (no "try the next slot") — if we hit the window exactly, we should beat other users to the spot.
- **If the booking succeeds**, also notify me (simple confirmation).
- **Config should be editable** without rebuilding the binary (YAML or JSON file).

### Deployment target

- Linux VPS I already own. Prefer a single Go binary + systemd service/timer.
- Build and test locally on Windows (my dev machine) before deploying.

### First-pass scope (don't touch the Arbox API yet)

The goal of the first pass is to **nail down the data model and CLI UX** before touching Arbox. Please build:

1. **`config.yaml`** — pre-populated with my weekly schedule above. Schema should include per-day time, enabled/disabled, and room for future fields (e.g., `coach_preference`, `studio_id`).
2. **`arbox schedule list`** — prints the next 7 days of planned bookings and the exact datetime each booking window opens (respecting the 24h / 48h-for-Sunday rule).
3. **`arbox schedule validate`** — sanity-checks the config (valid times, no duplicates, known days, etc.).
4. **Booker stub** — a `book(class)` function with a clear TODO for the Arbox API call. Include a dry-run mode so I can see what would happen without hitting a real API.
5. **Notification stub** — an interface (`Notifier`) with at least a stdout implementation; leave room for Telegram/email later.

### Later phases (do NOT build yet — just leave clean seams)

- **Phase 2:** Arbox API client (auth, list classes, book, join waitlist). Will come after I capture the network traffic.
- **Phase 3:** Scheduler daemon that sleeps until the next booking-window-open moment and fires the booking.
- **Phase 4:** VPS deployment (systemd unit, log rotation, credentials via env file).

### Ground rules

- **Language:** Go. Single binary. Standard library where reasonable; small focused deps are fine (e.g., `spf13/cobra`, `gopkg.in/yaml.v3`).
- **Keep it small.** No premature abstractions. No framework bloat.
- **Tests:** unit tests for the window-calculation logic (it's the trickiest part — DST, Sunday exception, timezone).
- **No secrets in the repo.** Credentials via env vars or a gitignored `.env`.

### What I want you to do right now

1. Ask me for the **project path** and **Go module name** (e.g. `github.com/assafmanzur/arbox-scheduler`).
2. Scaffold the project (go.mod, folder layout, `config.yaml`, commands, stubs).
3. Implement `schedule list` and `schedule validate` end-to-end so I can run them against my real config.
4. Show me the output of `arbox schedule list` so I can sanity-check the window math.

---

## Notes to self

- When you capture Arbox network traffic, save the full cURL of:
  - Login / token refresh request
  - List-classes request (for a given date range)
  - Book-class request
  - Join-waitlist request
  - Cancel-booking request (useful for testing)
- Paste those cURLs back into Claude to drive the Phase 2 API client.
