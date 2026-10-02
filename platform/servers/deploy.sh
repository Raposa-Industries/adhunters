#!/usr/bin/env bash
# Deploys this checkout and its bin/ to the boxes, data box first: copies them
# to ~/adhunters over Tailscale and runs deploy-box.sh there, which installs
# the build with setup.sh and puts the previous one back if a service does
# not come up. Stops at the first box that fails, so the others keep the
# build they have.
#
# Run from the repository root after the build line (platform/OPERATIONS.md).
# The deploy job (.github/workflows/deploy.yml) runs it on every merge to main.
#
#   platform/servers/deploy.sh [BOX...]     # default: data worker standby
#
# DEPLOY_SSH_OPTS adds ssh options.
set -euo pipefail

cd "$(dirname "$0")/../.."
[ -x bin/launch-web ] || { echo "bin/ holds no build: run the build line first" >&2; exit 2; }

tailnet=${TAILNET:-taila895a4.ts.net}
ssh="ssh -o BatchMode=yes -o ConnectTimeout=20 ${DEPLOY_SSH_OPTS:-}"
boxes=("$@")
[ ${#boxes[@]} -gt 0 ] || boxes=(data worker standby)

for box in "${boxes[@]}"; do
    host=admin@adhunters-$box.$tailnet
    echo "######## $box ($(git rev-parse --short HEAD))"
    rsync -az --exclude .git --exclude platform/observe/observe.env -e "$ssh" ./ "$host:adhunters/"
    $ssh "$host" 'sudo bash ~/adhunters/platform/servers/deploy-box.sh'
done
