# Deploying arbox-scheduler to Fly.io

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

## 3. Set secrets (Arbox credentials)

These are encrypted at rest by Fly and never end up in the image.

```bash
fly secrets set \
  ARBOX_EMAIL='you@example.com' \
  ARBOX_PASSWORD='your-arbox-password'
```

That's all the secrets you need. On first boot the daemon will call
`/api/v2/user/login` using them, discover your box + locations, and write
the access/refresh tokens to `/data/.env`. From then on only the refresh
token is used to stay logged in.

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
  next window opens in 14h23m07s @ 2026-04-18 07:30 IDT — Saturday 08:30 (pri=1, cat="Hall A")
```

Open a shell on the machine:

```bash
fly ssh console
# inside:
/app/arbox me
/app/arbox schedule resolve --days 7
cat /data/.env   # tokens are here, owned by the `arbox` user
```

---

## Day-2 operations

| Task                             | Command                                    |
| -------------------------------- | ------------------------------------------ |
| Push new code                    | `fly deploy`                               |
| Restart the daemon               | `fly machine restart`                      |
| Tail logs                        | `fly logs`                                 |
| Change a secret                  | `fly secrets set ARBOX_PASSWORD=...`       |
| Shell in                         | `fly ssh console`                          |
| Scale to 0 (pause the scheduler) | `fly scale count 0`                        |
| Scale back to 1                  | `fly scale count 1`                        |
| Destroy everything               | `fly apps destroy arbox-scheduler`         |

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

**"Out of memory" when building**
Shouldn't happen with 256 MB for this tiny binary, but if it does, bump
memory in `fly.toml`:

```toml
[[vm]]
  size   = "shared-cpu-1x"
  memory = "512mb"
```

Then `fly deploy` again.
