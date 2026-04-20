# arbox-scheduler HTTP API

A small REST surface, designed for LLM agents (nanobot / Claude / OpenAI
tool-calling) to drive booking, cancellation, waitlist, pause/resume, and
plan editing on a single Arbox member account. It runs in the same Go
process as the daemon — no separate service to operate.

The OpenAPI 3.1 spec is served at [`/api/v1/openapi.json`](#openapi).

---

## Authentication

Two bearer tokens, both 32+ random bytes (use `openssl rand -hex 32`):

| Env var | Purpose |
|---|---|
| `ARBOX_API_READ_TOKEN`  | Read-only. Required to call any GET endpoint. |
| `ARBOX_API_ADMIN_TOKEN` | Admin. Required for every mutation. Also accepted on every read endpoint. |

Set them in the env file the daemon reads. On Oracle that's
`~/arbox/data/.env`:

```bash
ssh oracle-vps 'cat >> ~/arbox/data/.env' <<EOF
ARBOX_API_READ_TOKEN=$(openssl rand -hex 32)
ARBOX_API_ADMIN_TOKEN=$(openssl rand -hex 32)
EOF
ssh oracle-vps 'sudo systemctl restart arbox'
```

Pass on every request:

```
Authorization: Bearer <token>
```

If both env vars are unset, the HTTP server **does not start** — daemon
keeps doing everything else, but the API is dark by design.

### Mutation safety: `?confirm=1`

Every mutation also requires `?confirm=1` on the URL on top of the admin
token. Without it, the handler returns `200` with a `{"actual_send": false}`
dry-run preview and writes a `confirm:false` audit-log line, but does not
call Arbox.

---

## Rate limits

Per-token bucket: **60 req/min, burst 30** (`golang.org/x/time/rate`).

Headers on every response:

```
X-RateLimit-Limit:     60
X-RateLimit-Remaining: <int>
X-RateLimit-Reset:     <unix ts when bucket is full again>
```

On 429 we additionally set `Retry-After: <seconds>`.

---

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| GET  | `/api/v1/healthz`                | — | Unauthenticated liveness probe (safe to poll from systemd / nanobot). |
| GET  | `/api/v1/openapi.json`           | — | OpenAPI 3.1 spec. |
| GET  | `/api/v1/version`                | read | Build, gym, tz, pause snapshot. |
| GET  | `/api/v1/selftest?days=7`        | read | Health checks. |
| GET  | `/api/v1/status?days=7`          | read | Saved plan + live availability. |
| GET  | `/api/v1/classes?date=…&filter=…`| read | Classes for one calendar day. |
| GET  | `/api/v1/morning?from=6&to=12&days=1` | read | Morning window. |
| GET  | `/api/v1/evening?from=16&to=22&days=1`| read | Evening window. |
| GET  | `/api/v1/bookings?days=14`       | read | Confirmed BOOKED + WAITLIST classes. |
| GET  | `/api/v1/plan`                   | read | Merged config (config.yaml + user_plan.yaml). |
| GET  | `/api/v1/attempts?days=30`       | read | Recent terminal booking attempts. |
| GET  | `/api/v1/audit?limit=50&since=…` | read | Audit log tail (newest first). |
| POST | `/api/v1/book?confirm=1`         | admin | Book a class. |
| POST | `/api/v1/cancel?confirm=1`       | admin | Cancel a booking. |
| POST | `/api/v1/waitlist?confirm=1`     | admin | Join the waitlist. |
| POST | `/api/v1/waitlist/leave?confirm=1`| admin | Leave the waitlist. |
| POST | `/api/v1/pause?confirm=1`        | admin | Pause auto-booking. |
| POST | `/api/v1/resume?confirm=1`       | admin | Clear pause. |
| POST | `/api/v1/plan/day?confirm=1`     | admin | Update one weekday in user_plan.yaml. |
| POST | `/api/v1/plan/clear?confirm=1`   | admin | Set one weekday to "rest day". |

---

## Examples

> Throughout, replace `$READ` / `$ADMIN` with your tokens and
> `$BASE` with the daemon's listen address. On the production Oracle VM
> that's `http://127.0.0.1:8080` (loopback-only, no TLS needed); locally
> it's also usually `http://127.0.0.1:8080`.

### `GET /api/v1/healthz`

```bash
curl -s $BASE/api/v1/healthz
```

```json
{ "ok": true }
```

### <a id="openapi"></a> `GET /api/v1/openapi.json`

```bash
curl -s $BASE/api/v1/openapi.json | jq '.info'
```

### `GET /api/v1/version`

```bash
curl -s -H "Authorization: Bearer $READ" $BASE/api/v1/version
```

```json
{
  "version": "v0.6.0",
  "rev": "abc1234",
  "gym": "Rose Valley",
  "tz": "Asia/Jerusalem",
  "locations_box_id": 1575,
  "lookahead_days": 7,
  "pause": { "active": false }
}
```

### `GET /api/v1/selftest?days=7`

```bash
curl -s -H "Authorization: Bearer $READ" "$BASE/api/v1/selftest?days=7"
```

```json
{
  "checks": [
    { "name": "Config + TZ", "ok": true, "detail": "tz=Asia/Jerusalem now=…", "latency_ms": 0 }
  ],
  "next_bookings": [
    "Sun 26 Apr 08:00 — Hall B then Hall A · window Fri 24 Apr 08:00 (in 1d2h)"
  ]
}
```

### `GET /api/v1/status?days=7`

```bash
curl -s -H "Authorization: Bearer $READ" "$BASE/api/v1/status?days=7"
```

```json
{
  "now": "2026-04-19T11:42:01+03:00",
  "timezone": "Asia/Jerusalem",
  "pause": { "active": false },
  "days": [
    {
      "weekday": "sunday",
      "date": "2026-04-26",
      "enabled": true,
      "options": [{ "time": "08:00", "category": "Hall B" }],
      "live": [ /* class objects */ ]
    }
  ]
}
```

### `GET /api/v1/classes?date=2026-04-19&filter=true`

```bash
curl -s -H "Authorization: Bearer $READ" "$BASE/api/v1/classes?date=2026-04-19"
```

```json
{
  "date": "2026-04-19",
  "classes": [
    {
      "schedule_id": 49049252,
      "time": "08:00", "end_time": "09:00",
      "category": "CrossFit- Hall A",
      "free": 4, "registered": 14, "max_users": 18, "stand_by": 0,
      "you_status": "BOOKED",
      "coach": "Jane"
    }
  ]
}
```

### `GET /api/v1/morning?from=6&to=12&days=2`

Same shape as `/evening`, with one entry per day in the requested window.

### `GET /api/v1/bookings?days=14`

```bash
curl -s -H "Authorization: Bearer $READ" "$BASE/api/v1/bookings?days=14"
```

```json
{
  "bookings": [
    { "schedule_id": 49049252, "when": "2026-04-19T08:00:00+03:00", "category": "Hall A", "status": "BOOKED" }
  ]
}
```

### `GET /api/v1/plan`

Returns the merged `config.yaml` + `user_plan.yaml` as JSON.

### `GET /api/v1/attempts?days=30`

Reads `booking_attempts.json` filtered to the last N days, newest first.

### `GET /api/v1/audit?limit=50&since=2026-04-19T00:00:00Z`

```json
{
  "entries": [
    {
      "ts":          "2026-04-19T11:42:01.123Z",
      "route":       "/api/v1/book",
      "token_kind":  "admin",
      "args":        { "schedule_id": 49049252 },
      "confirm":     true,
      "result":      "BOOKED",
      "schedule_id": 49049252,
      "latency_ms":  342,
      "client_ip":   "1.2.3.4",
      "http_status": 200
    }
  ]
}
```

### `POST /api/v1/book` (dry-run)

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"schedule_id": 49049252}' \
  $BASE/api/v1/book
```

```json
{
  "would_send":  { "schedule_id": 49049252 },
  "actual_send": false,
  "reason":      "add ?confirm=1 to the URL to actually send this request to Arbox",
  "route":       "/api/v1/book"
}
```

### `POST /api/v1/book?confirm=1`

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"schedule_id": 49049252}' \
  "$BASE/api/v1/book?confirm=1"
```

```json
{
  "result": "BOOKED",
  "request": { "schedule_id": 49049252 },
  "upstream": {
    "url": "https://apiappv2.arboxapp.com/api/v2/scheduleUser/insert",
    "method": "POST",
    "status_code": 200,
    "sent": true,
    "request_json": "{ … }"
  }
}
```

### `POST /api/v1/cancel?confirm=1`

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"schedule_user_id": 12345}' \
  "$BASE/api/v1/cancel?confirm=1"
```

### `POST /api/v1/waitlist?confirm=1` and `POST /api/v1/waitlist/leave?confirm=1`

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"schedule_id": 49049252}' \
  "$BASE/api/v1/waitlist?confirm=1"

curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"standby_id": 9999}' \
  "$BASE/api/v1/waitlist/leave?confirm=1"
```

### `POST /api/v1/pause?confirm=1`

Two body shapes:

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"duration": "3d", "reason": "vacation"}' \
  "$BASE/api/v1/pause?confirm=1"

curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"until": "2026-04-25T08:00:00+03:00"}' \
  "$BASE/api/v1/pause?confirm=1"
```

### `POST /api/v1/resume?confirm=1`

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" "$BASE/api/v1/resume?confirm=1"
```

### `POST /api/v1/plan/day?confirm=1`

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"weekday":"monday","options":[{"time":"08:30","category":"Hall B"},{"time":"08:30","category":"Hall A"}]}' \
  "$BASE/api/v1/plan/day?confirm=1"
```

### `POST /api/v1/plan/clear?confirm=1`

```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"weekday":"wednesday"}' \
  "$BASE/api/v1/plan/clear?confirm=1"
```

---

## Error shape

Non-2xx responses are JSON:

```json
{ "error": "schedule_id required and must be > 0", "code": "bad_request" }
```

Common `code` values: `unauthorized`, `forbidden`, `bad_request`,
`rate_limited`, `upstream`, `fs_error`, `panic`.

---

## Audit log

Every mutation request — dry-run or real — writes one JSON line to
`audit.jsonl` next to the `.env` file (so `~/arbox/data/audit.jsonl` on
Oracle). Override with `ARBOX_AUDIT_LOG`. The file rotates to `<path>.1`
when it exceeds 10 MB; one backup is kept. Mode `0o600`.

Read recent entries via `/api/v1/audit`:

```bash
curl -s -H "Authorization: Bearer $READ" "$BASE/api/v1/audit?limit=20"
```

Filter by time:

```bash
curl -s -H "Authorization: Bearer $READ" \
  "$BASE/api/v1/audit?since=$(date -u -v-1H +%FT%TZ)"
```

---

## Concurrency notes

Real (non-dry-run) booking handlers (`/book`, `/cancel`, `/waitlist`,
`/waitlist/leave`) acquire the same `bookerMu` mutex used by the proactive
scheduler, so the two paths can never fire `BookClass` for the same
`schedule_id` simultaneously. The persisted `booking_attempts.json` keeps
terminal outcomes across restarts.
