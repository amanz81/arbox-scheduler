#!/bin/bash
# Manual deploy from this Mac → Oracle VM.
#
# Mirrors .github/workflows/oracle-deploy.yml, but routes through the
# oracle-vps SSH alias (which uses the existing ProxyJump via sshuser@home-pc)
# so the Mac doesn't need direct outbound port 22 to the public internet.
# Useful for fast iteration without pushing + waiting for CI.
#
# Requires the ~/.ssh/config entries this repo's SKILL.md documents:
#   Host oracle-vps   ProxyJump windows-jump, key id_ed25519 orcal_vps_new
#   Host windows-jump HostName 192.168.68.100, User sshuser
#
# Usage:
#   bash scripts/deploy-oracle.sh          # deploy current HEAD
#   bash scripts/deploy-oracle.sh --test   # also run go test ./... first

set -euo pipefail
cd "$(dirname "$0")/.."

want_test=0
for arg in "$@"; do
  case "$arg" in
    --test|-t) want_test=1 ;;
    *)         echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

REV=$(git rev-parse --short HEAD)
BRANCH=$(git rev-parse --abbrev-ref HEAD)
DIRTY=""
if ! git diff-index --quiet HEAD --; then
  DIRTY=" (dirty)"
fi

echo "==> deploying arbox-scheduler  rev=$REV  branch=$BRANCH$DIRTY"

if [ "$want_test" -eq 1 ]; then
  echo "==> go test ./..."
  go test ./...
fi

echo "==> cross-compile linux/amd64 (arbox + arbox-mcp)"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -X main.Version=$REV" -o "$TMP/arbox" ./cmd/arbox
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -X main.Version=$REV" -o "$TMP/arbox-mcp" ./cmd/arbox-mcp
ls -la "$TMP/arbox" "$TMP/arbox-mcp"

echo "==> scp to oracle-vps:~/arbox/bin/*.new"
scp -q "$TMP/arbox"     oracle-vps:"~/arbox/bin/arbox.new"
scp -q "$TMP/arbox-mcp" oracle-vps:"~/arbox/bin/arbox-mcp.new"

echo "==> atomic swap + restart + smoke"
ssh oracle-vps bash -s <<'REMOTE'
set -euo pipefail
cd ~/arbox
# arbox-mcp first — it's a short-lived subprocess spawned by nanobot on
# demand, so there's no running instance to conflict with a rename.
mv bin/arbox-mcp.new bin/arbox-mcp
chmod +x bin/arbox-mcp
# Then the long-running daemon binary.
mv bin/arbox.new bin/arbox
chmod +x bin/arbox
echo "  new binaries:"
ls -la bin/arbox bin/arbox-mcp
./bin/arbox --help >/dev/null
sudo systemctl restart arbox
sleep 3
sudo systemctl --no-pager status arbox | head -10
systemctl is-active arbox
echo
echo "  selftest:"
set -a; . ./data/.env; set +a
TZ=Asia/Jerusalem ARBOX_ENV_FILE=~/arbox/data/.env ./bin/arbox selftest --days 3
echo
echo "  arbox-mcp version:"
./bin/arbox-mcp --help 2>/dev/null | head -3 || echo "    (arbox-mcp is an MCP stdio server; no CLI output by design)"
REMOTE

echo
echo "✓ deployed rev $REV (arbox + arbox-mcp)"
