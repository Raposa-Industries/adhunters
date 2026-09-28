#!/usr/bin/env bash
# Writes pgBackRest's state as metrics for Alloy's textfile collector: when
# each kind of backup last finished, and whether the repository answered.
# Run as root after every backup (the backup units' ExecStopPost).
set -uo pipefail
dir=/var/lib/adhunters/textfile
out=$dir/pgbackrest.prom
tmp=$(mktemp "$dir/.pgbackrest.XXXXXX")

ok=0
if info=$(sudo -u postgres pgbackrest --stanza=adhunters --output=json info 2>/dev/null) &&
    [ "$(jq -r '.[0].status.code' <<<"$info")" = 0 ]; then
    ok=1
fi
{
    echo "# HELP pgbackrest_info_ok 1 when the backup repository answered and the stanza is healthy."
    echo "# TYPE pgbackrest_info_ok gauge"
    echo "pgbackrest_info_ok $ok"
    if [ "$ok" = 1 ]; then
        echo "# HELP pgbackrest_last_backup_timestamp_seconds When the newest backup of each type finished."
        echo "# TYPE pgbackrest_last_backup_timestamp_seconds gauge"
        jq -r '.[0].backup | group_by(.type)[] | max_by(.timestamp.stop)
            | "pgbackrest_last_backup_timestamp_seconds{type=\"\(.type)\"} \(.timestamp.stop)"' <<<"$info"
    fi
} >"$tmp"
chmod 0644 "$tmp"
mv "$tmp" "$out"
