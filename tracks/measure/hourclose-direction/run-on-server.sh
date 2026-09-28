#!/usr/bin/env bash
# Runs the hour close and Direction timing on a throwaway server, from your
# own machine: copies the newest archived day of sightings from bigworker (read
# only), runs bench.sh there, and brings results.md back here.
#
#   tracks/measure/hourclose-direction/run-on-server.sh <server-ip>
#
# The server should be a fresh Ubuntu 24.04 CX43 you can reach as root. The
# README says how to rent one and delete it after.
set -euo pipefail
server=${1:?usage: run-on-server.sh <server-ip>}
HOST=${HOST:-bigworker}
ARCHIVE=${ARCHIVE:-/opt/backups/spy-sightings}
cd "$(dirname "$0")"

file=${DAY_FILE:-$(ssh "$HOST" "ls -1 $ARCHIVE/sighting_*.tsv.gz | tail -1")}
name=$(basename "$file")
echo "== checking $name on $HOST"
ssh "$HOST" "cd $ARCHIVE && sha256sum -c $name.sha256"
echo "== copying it to $server"
ssh "root@$server" "mkdir -p /root/bench"
ssh "$HOST" "cat $file" | ssh "root@$server" "cat > /root/bench/$name"
scp -q ./*.sql bench.sh "root@$server:/root/bench/"
echo "== running bench.sh on $server (installs PostgreSQL 17 first; takes a while)"
ssh "root@$server" "cd /root/bench && ./bench.sh /root/bench/$name"
out=results-${name%.tsv.gz}.md
scp -q "root@$server:/root/bench/results.md" "$out"
echo "== results saved to $(pwd)/$out"
