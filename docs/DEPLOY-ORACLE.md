# Deploying arbox-scheduler to Oracle Free Tier

The author's production target. $0/month, reliable Cloudflare passage,
and a generous always-free tier (AMD micro + 4× Ampere A1). See the main
[README](../README.md#quick-start-oracle-vm--current-production) for a
side-by-side comparison with other cheap hosts (Netcup, Hetzner).

## Why Oracle specifically

Arbox's API is behind Cloudflare with Bot Fight Mode on. Cloudflare
scores inbound connections by ASN; some hosting-heavy ASNs (Fly.io,
parts of Hetzner) get challenged or 403'd out of the box. Oracle Cloud
Frankfurt has passed cleanly in every test we've run. If you're picking
a different host, verify the daemon can reach `apiappv2.arboxapp.com`
with a simple `curl -sI` against the login endpoint before you commit.

## Topology

```
Mac ─── ssh (proxyjump via home-pc) ──▶ ubuntu@152.70.44.28  (Oracle Free Tier)
                                            ├── nanobot (python3.11)
                                            └── arbox-scheduler (systemd unit)
                                                 └─ /home/ubuntu/arbox/data/*
```

- **systemd unit:** `/etc/systemd/system/arbox.service`
- **install dir:** `/home/ubuntu/arbox/`
- **state:** `/home/ubuntu/arbox/data/` (`.env`, `user_plan.yaml`, `pause.json`, `booking_attempts.json`)
- **config:** `/home/ubuntu/arbox/config.yaml` (from repo)

## Automated deploy — GitHub Actions

`.github/workflows/oracle-deploy.yml` runs on every push to `main`:

1. `go test ./...`
2. Cross-compile `GOOS=linux GOARCH=amd64` → `bin/arbox`
3. `scp` to Oracle as `~/arbox/bin/arbox.new`
4. Atomic swap + `sudo systemctl restart arbox`
5. Remote `arbox selftest --days 3` must pass (fails the job otherwise)

### One-time setup

1. Generate a deploy-only ed25519 keypair (Mac):

   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/arbox-deploy-ci -N "" -C "github-actions@arbox-scheduler-deploy"
   ```

2. Authorize the public key on Oracle (via the existing tunnel):

   ```bash
   ssh oracle-vps "cat >> ~/.ssh/authorized_keys" < ~/.ssh/arbox-deploy-ci.pub
   ```

3. Add three GitHub repo secrets (**Settings → Secrets and variables → Actions → New repository secret**):

   | Name             | Value                                                          |
   | ---------------- | -------------------------------------------------------------- |
   | `ORACLE_HOST`    | `152.70.44.28`                                                 |
   | `ORACLE_USER`    | `ubuntu`                                                       |
   | `ORACLE_SSH_KEY` | paste the **private** key body (`cat ~/.ssh/arbox-deploy-ci`)  |

   Using `gh` CLI:

   ```bash
   gh secret set ORACLE_HOST    -b '152.70.44.28'
   gh secret set ORACLE_USER    -b 'ubuntu'
   gh secret set ORACLE_SSH_KEY < ~/.ssh/arbox-deploy-ci
   ```

### How it behaves

| Trigger           | Jobs run        | Notes                                          |
| ----------------- | --------------- | ---------------------------------------------- |
| Push to `main`    | test + deploy   | Docs-only pushes (`*.md`, `docs/**`) skipped   |
| Pull request      | test only       | No deploy; CI gate before merge                |
| Manual dispatch   | test + deploy   | Actions tab → "Run workflow" (latest `main`)   |

## Manual deploy from Mac

`scripts/deploy-oracle.sh` does the same steps, routed through your
`oracle-vps` SSH alias. Useful when iterating fast without a PR:

```bash
bash scripts/deploy-oracle.sh          # deploy current HEAD
bash scripts/deploy-oracle.sh --test   # run go test ./... first
```

The script does not require any GitHub secrets — it reuses your Mac SSH
config to reach Oracle.

## Day-2 operations

```bash
# status
ssh oracle-vps 'sudo systemctl status arbox --no-pager | head -15'

# live logs
ssh oracle-vps 'sudo journalctl -u arbox -f --no-pager'

# recent 80 log lines
ssh oracle-vps 'sudo journalctl -u arbox -n 80 --no-pager'

# inspect state files
ssh oracle-vps 'cat ~/arbox/data/booking_attempts.json'
ssh oracle-vps 'cat ~/arbox/data/pause.json 2>/dev/null || echo not-paused'

# restart (no code change)
ssh oracle-vps 'sudo systemctl restart arbox'

# stop / start
ssh oracle-vps 'sudo systemctl stop arbox'
ssh oracle-vps 'sudo systemctl start arbox'

# manual selftest
ssh oracle-vps 'cd ~/arbox && set -a; . ./data/.env; set +a; TZ=Asia/Jerusalem ARBOX_ENV_FILE=~/arbox/data/.env ./bin/arbox selftest'
```

## Rollback

Two options depending on how fast you want it:

- **Revert on GitHub → Actions redeploys the previous commit** (standard; ~1 min).
- **Local manual revert** (~10 s): on Mac, check out a previous SHA and run
  `bash scripts/deploy-oracle.sh`. The old binary replaces the new one;
  systemd restarts; `booking_attempts.json` and `user_plan.yaml` on the
  volume are untouched.

## Capacity

The Oracle VM is the free-tier AMD shape (2 CPU, 1 GB RAM, 45 GB disk).
Currently sharing it with nanobot (`python3.11 -m nanobot gateway`). Arbox
daemon idles at ~7 MB RSS, so resource pressure is a non-issue.

## Firewall

Oracle's VCN default Security List allows TCP/22 from `0.0.0.0/0`. The
deploy-only SSH key is the only authenticator that can land the binary,
and `sudo systemctl restart arbox` is the only privileged command that key
actually performs. If you want to tighten further later:

- Restrict `authorized_keys` with `command="…"` to limit the deploy key
  to exactly the upload + restart sequence.
- Limit the Security List ingress to GitHub's published Actions CIDRs
  (`https://api.github.com/meta` → `.actions`). The list is wide (~4000
  CIDRs) and churns weekly, so most people rely on key security instead.
