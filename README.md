# arbox-scheduler

A Go CLI that auto-books weekly CrossFit classes on [Arbox](https://arboxapp.com/) the moment the booking window opens.

- **Booking windows:** 24h before class start, except **Sunday → 48h**.
- **Timezone:** `Asia/Jerusalem` (configurable).

## Status

This is **Phase 1**: config schema + CLI UX. No real Arbox API calls yet.

| Phase | Goal                                                  | Status |
|-------|-------------------------------------------------------|--------|
| 1     | Config, `schedule list`/`validate`, booker stub       | ✅ done |
| 2     | Arbox API client (auth, list, book, waitlist, cancel) | 🔜     |
| 3     | Scheduler daemon that fires at window-open time       | 🔜     |
| 4     | VPS deploy (systemd unit + log rotation + env file)   | 🔜     |

## Install / build

Requires Go **1.24+**.

```bash
go build -o bin/arbox ./cmd/arbox
```

## Usage

```bash
# Check the config file parses and is semantically valid.
./bin/arbox schedule validate

# Show the next 7 days of planned bookings and when each booking window opens.
./bin/arbox schedule list
./bin/arbox schedule list --days 14

# Dry-run the booker stub (Phase 2 will wire it to the Arbox API).
./bin/arbox book --dry-run
```

## Config

Edit `config.yaml`:

```yaml
timezone: Asia/Jerusalem
default_time: "09:00"
days:
  sunday:    { enabled: true,  time: "08:00" }
  monday:    { enabled: true,  time: "08:30" }
  tuesday:   { enabled: true,  time: "09:00" }
  wednesday: { enabled: false }
  thursday:  { enabled: true,  time: "08:30" }
  friday:    { enabled: true,  time: "11:00" }
  saturday:  { enabled: false }
```

No rebuild needed after changing the config.

## Tests

```bash
go test ./...
```

Focused tests live in `internal/schedule/` (window math, DST, Sunday 48h rule) and `internal/config/` (schema validation).

## Layout

```
cmd/arbox/           # cobra CLI entrypoint
internal/config/     # YAML load + validate
internal/schedule/   # booking-window math
internal/booker/     # Phase-1 stub; Phase-2 Arbox API lives here
internal/notify/     # Notifier interface + stdout impl
```

## Secrets

No secrets are committed. Phase 2 will read credentials from `.env` or real env vars. `.env` is gitignored.
