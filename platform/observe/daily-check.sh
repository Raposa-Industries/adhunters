#!/usr/bin/env bash
# Prints what fired and what failed in the last 24 hours, read from Grafana
# Cloud's Prometheus with a read-only token. Claude's daily check runs it from
# a cloud session, which cannot reach the boxes; a person can run it too.
#
#   GRAFANA_QUERY_URL    the stack's Prometheus URL followed by /api/prom
#   GRAFANA_QUERY_USER   the Prometheus user number
#   GRAFANA_QUERY_TOKEN  an access policy token with metrics:read only
#
#   ./daily-check.sh [hours]    (default 24)
#
# It only reads. Sentry's side (new issues) is read through Sentry itself.
set -euo pipefail
hours=${1:-24}
: "${GRAFANA_QUERY_URL:?set GRAFANA_QUERY_URL (see platform/observe/README.md, Daily check)}"
: "${GRAFANA_QUERY_USER:?set GRAFANA_QUERY_USER}"
: "${GRAFANA_QUERY_TOKEN:?set GRAFANA_QUERY_TOKEN}"

# q TITLE QUERY: prints the title, then one line per series: its labels
# (without __name__) and its value, largest first.
q() {
    local title=$1 query=$2 out
    out=$(curl -fsS --max-time 60 -u "$GRAFANA_QUERY_USER:$GRAFANA_QUERY_TOKEN" \
        --data-urlencode "query=$query" "${GRAFANA_QUERY_URL%/}/api/v1/query") || {
        printf '\n== %s\nquery failed\n' "$title"
        return 0
    }
    printf '\n== %s\n' "$title"
    jq -r '
        if .status != "success" then "error: \(.error)"
        elif (.data.result | length) == 0 then "none"
        else .data.result | sort_by(-(.value[1] | tonumber)) | .[] |
            ((.metric | del(.__name__) | to_entries | map("\(.key)=\(.value)") | join(" ")) + "  " +
             (.value[1] | tonumber | . * 100 | round / 100 | tostring))
        end' <<<"$out"
}

h="${hours}h"
echo "AdHunters daily check, the last $hours hours to $(date -u '+%Y-%m-%d %H:%M UTC')"
q "Alerts firing now" 'ALERTS{alertstate="firing", alertname!="Heartbeat"}'
q "Alerts that fired (minutes firing)" "sum by (alertname, service, task, box, msg) (count_over_time(ALERTS{alertstate=\"firing\", alertname!=\"Heartbeat\"}[$h]))"
q "Error lines by service and message" "sum by (service, msg) (increase(adhunters_log_errors_total[$h])) >= 1"
q "Failed task runs" "sum by (service, task) (increase(adhunters_task_runs_total{result=\"error\"}[$h])) >= 1"
q "Tasks late now (seconds since last success)" 'time() - adhunters_task_last_success_timestamp_seconds > adhunters_task_promise_seconds'
q "Restarts" "sum by (service, box) (changes(process_start_time_seconds{job=\"adhunters\"}[$h])) > 0"
q "Units failed now" 'node_systemd_unit_state{state="failed"} == 1'
q "Services not answering now" 'up{job="adhunters"} == 0'
q "Scrapes, successful share" "sum(increase(tracks_capture_scrapes_total{outcome=\"ok\"}[$h])) / sum(increase(tracks_capture_scrapes_total[$h]))"
q "Walks, failed share by box" "sum by (box) (increase(tracks_walker_walks_total{outcome!=\"ok\"}[$h])) / sum by (box) (increase(tracks_walker_walks_total[$h]))"
q "Walker, time to walk what waits now (seconds; WalkerBehind over 7200)" 'max(tracks_walker_backlog) / (sum(rate(tracks_walker_walks_total[15m])) > 0)'
q "Walker, longest wait of one ad (seconds, information only)" 'max by (box) (tracks_walker_oldest_overdue_seconds)'
q "Raposa steps by result" "sum by (result) (increase(raposa_steps_total[$h]))"
q "Raposa automatic quick runs queued" "sum(increase(raposa_auto_queued_total[$h]))"
q "Loader, most behind (seconds)" "max_over_time(max(tracks_loader_lag_seconds)[$h:5m])"
q "Disk free, share" 'min by (box) (node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})'
