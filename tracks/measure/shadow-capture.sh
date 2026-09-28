#!/usr/bin/env bash
# Runs tracks-capture in shadow on bigworker, beside the live collector, to
# measure raw bytes per scrape. Run it from your own machine, in a checkout of
# this repo, where `ssh bigworker` works (as the collector's ops/deploy.sh).
#
#   tracks/measure/shadow-capture.sh start    build, copy, start for 24 h
#   tracks/measure/shadow-capture.sh status   is it running, last log lines
#   tracks/measure/shadow-capture.sh stats    bytes per scrape so far
#   tracks/measure/shadow-capture.sh stop     stop early (it stops by itself)
#   tracks/measure/shadow-capture.sh clean    stop and delete /opt/tracks-shadow
#
# What it touches on bigworker: /opt/tracks-shadow only, plus a transient
# systemd unit, tracks-capture-shadow, that ends by itself after FOR. It reads
# the collector's publishers.yaml and proxies.env and never writes them. It
# does not stop, restart or reconfigure the collector, and uses no database.
#
# It shares the collector's proxy lines, so it runs gently by default: one
# worker and a 10 s pause, about 360 scrapes an hour, a few percent on top of
# the collector's rate. Bytes per scrape do not depend on the rate, and stats
# recompresses each hour as one file, which is about what one minute's file
# holds at the full rate. WORKERS, THROTTLE and FOR override.
set -euo pipefail

HOST=${HOST:-bigworker}
DIR=/opt/tracks-shadow
UNIT=tracks-capture-shadow
FOR=${FOR:-24h}
WORKERS=${WORKERS:-1}
THROTTLE=${THROTTLE:-10s}
COLLECTOR=/opt/adhunters-collector

cd "$(dirname "$0")/.."
[[ $FOR =~ ^[0-9]+h$ ]] || { echo "FOR must be whole hours, like 24h"; exit 2; }

build() {
  version=$(git rev-parse --short HEAD)
  echo "== building tracks-capture $version for linux/amd64"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o /tmp/tracks-capture ./cmd/tracks-capture
}

case ${1:-} in
start)
  build
  echo "== copying to $HOST:$DIR"
  ssh "$HOST" "mkdir -p $DIR/spool && ! systemctl is-active --quiet $UNIT" ||
    { echo "$UNIT is already running on $HOST; run stop first"; exit 1; }
  scp -q /tmp/tracks-capture "$HOST:$DIR/tracks-capture"
  # scp keeps an existing file's mode, so set it, and prove the binary runs
  # there before handing it to systemd.
  ssh "$HOST" "chmod 0755 $DIR/tracks-capture && $DIR/tracks-capture version"
  echo "== starting $UNIT for $FOR ($WORKERS worker, $THROTTLE pause)"
  # Sandboxed: it can write only its own folder, and is capped well below
  # what the collector and Raposa use.
  ssh "$HOST" systemd-run --unit "$UNIT" --collect \
    -p RuntimeMaxSec=$(( ${FOR%h} * 3600 + 600 )) \
    -p MemoryMax=512M -p CPUQuota=100% -p Nice=10 \
    -p ProtectSystem=strict -p ReadWritePaths=$DIR -p ProtectHome=yes -p PrivateTmp=yes -p NoNewPrivileges=yes \
    -p KillSignal=SIGTERM -p TimeoutStopSec=45 \
    --setenv=OPS_ADDR=127.0.0.1:9101 --working-directory=$DIR \
    $DIR/tracks-capture run \
      -targets $COLLECTOR/config/publishers.yaml \
      -lines $COLLECTOR/secrets/proxies.env \
      -spool $DIR/spool -instance shadow \
      -workers "$WORKERS" -throttle "$THROTTLE" -for "$FOR"
  sleep 5
  ssh "$HOST" "systemctl is-active $UNIT && journalctl -u $UNIT -n 3 --no-pager -o cat"
  echo "Started. It stops by itself after $FOR. Then run: $0 stats"
  ;;
status)
  ssh "$HOST" "systemctl status $UNIT --no-pager -n 0 || true; journalctl -u $UNIT -n 5 --no-pager -o cat; du -sh $DIR/spool"
  ;;
stats)
  # Built fresh from this checkout, beside the running binary, so a newer
  # stats works on a run started from an older commit.
  build
  scp -q /tmp/tracks-capture "$HOST:$DIR/tracks-capture-stats"
  ssh "$HOST" "chmod 0755 $DIR/tracks-capture-stats && nice -n 10 $DIR/tracks-capture-stats stats -rate 14000 $DIR/spool"
  ;;
stop)
  ssh "$HOST" "systemctl stop $UNIT"
  ;;
clean)
  ssh "$HOST" "systemctl stop $UNIT 2>/dev/null || true; rm -rf $DIR"
  ;;
*)
  sed -n '2,19p' "$0"
  exit 2
  ;;
esac
