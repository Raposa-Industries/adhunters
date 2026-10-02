#!/usr/bin/env bash
# Checks or uploads the alert rules and the Alertmanager config.
#
#   ./push.sh check   render with stand-in secrets and validate (promtool, amtool, jq)
#   ./push.sh push    render with observe.env and upload rules, Alertmanager
#                     config (mimirtool) and dashboards (Grafana's API) to Grafana Cloud
#
# Rules: rules/*.yaml, one namespace per file. Tests: tests/rules_test.yaml.
# Every alert must carry a tier label and a runbook that exists.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
cmd=${1:-}

# render DIR: writes alertmanager.yaml and telegram.tmpl into DIR with the
# @@NAME@@ values taken from the environment.
render() {
    local out=$1 name val cfg
    cfg=$(cat "$here/alertmanager/alertmanager.yaml")
    for name in TELEGRAM_BOT_TOKEN TELEGRAM_CHAT_ID BETTERSTACK_HEARTBEAT_URL; do
        val=${!name}
        cfg=${cfg//@@$name@@/$val}
    done
    printf '%s\n' "$cfg" >"$out/alertmanager.yaml"
    cp "$here/alertmanager/telegram.tmpl" "$out/"
}

# runbooks: each alert has a tier and a runbook_url pointing at a file here.
runbooks() {
    local fail=0 alerts urls tiers beats resolved url f
    alerts=$(grep -h '^ *- alert:' "$here"/rules/*.yaml | wc -l)
    urls=$(grep -h 'runbook_url:' "$here"/rules/*.yaml | wc -l)
    tiers=$(grep -hE '^ *tier: (page|chat|heartbeat)$' "$here"/rules/*.yaml | wc -l)
    if [ "$alerts" != "$urls" ] || [ "$alerts" != "$tiers" ]; then
        echo "$alerts alerts but $urls runbook_url and $tiers tier labels" >&2
        fail=1
    fi
    # Every alert that reaches Telegram says in "resolved" what clearing
    # means (telegram.tmpl shows it instead of the summary); the heartbeat
    # never clears.
    beats=$(grep -hE '^ *tier: heartbeat$' "$here"/rules/*.yaml | wc -l)
    resolved=$(grep -h '^ *resolved:' "$here"/rules/*.yaml | wc -l)
    if [ "$resolved" != $((alerts - beats)) ]; then
        echo "$((alerts - beats)) alerts reach Telegram but $resolved have a resolved annotation" >&2
        fail=1
    fi
    while read -r url; do
        f=${url#https://github.com/Raposa-Industries/adhunters/blob/main/}
        [ -f "$repo/$f" ] || { echo "missing runbook $f" >&2; fail=1; }
    done < <(grep -ho 'runbook_url: .*' "$here"/rules/*.yaml | cut -d' ' -f2)
    [ "$fail" = 0 ] && echo "runbooks ok: $alerts alerts, each with a tier and a runbook, and a resolved line where it can clear"
    return $fail
}

# units_watched: every unit setup.sh runs a service in is one Alloy watches
# for UnitFailed and ships the journal of. A unit missing from these lists
# fails, crashes and logs errors where no alert or search sees it.
units_watched() {
    local include keep unit fail=0
    include=$(sed -n 's/^ *unit_include *= *"\(.*\)"$/\1/p' "$here/alloy/common.alloy" | sed 's/\\\\/\\/g')
    keep=$(sed -n 's/^ *regex *= *"\(.*\)"$/\1/p' "$here/alloy/common.alloy" | sed 's/\\\\/\\/g')
    [ -n "$include" ] && [ -n "$keep" ] || { echo "no unit_include or journal regex in common.alloy" >&2; return 1; }
    for unit in $(sed -n "/^ops_ports='/,/'\$/p" "$repo/platform/servers/setup.sh" | sed "s/^ops_ports='//" | awk '{print $1}') run-u1234; do
        printf '%s.service' "$unit" | grep -Eqx "$include" || { echo "Alloy's unit_include misses $unit" >&2; fail=1; }
        printf '%s.service;journal' "$unit" | grep -Eqx "$keep" || { echo "Alloy ships no journal for $unit" >&2; fail=1; }
    done
    [ "$fail" = 0 ] && echo "units ok: Alloy watches every unit setup.sh runs, and ships its journal"
    return $fail
}

# dashboards_ok: each dashboard is JSON with a fixed uid and a title.
dashboards_ok() {
    local f
    for f in "$here"/dashboards/*.json; do
        jq -e '(.uid | type == "string" and length > 0) and (.title | length > 0) and (.panels | length > 0)' "$f" >/dev/null ||
            { echo "bad dashboard $f" >&2; return 1; }
    done
    echo "dashboards ok"
}

case "$cmd" in
check)
    dashboards_ok
    runbooks
    units_watched
    promtool check rules "$here"/rules/*.yaml
    promtool test rules "$here"/tests/rules_test.yaml
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    TELEGRAM_BOT_TOKEN=123:stand-in TELEGRAM_CHAT_ID=-100123 \
        BETTERSTACK_HEARTBEAT_URL=https://example.com/heartbeat render "$tmp"
    amtool check-config "$tmp/alertmanager.yaml"
    ;;
push)
    env_file="$here/observe.env"
    [ -f "$env_file" ] || { echo "copy observe.env.example to observe.env and fill it in" >&2; exit 1; }
    if grep -q FILL_ME "$env_file"; then
        echo "observe.env still has FILL_ME values" >&2
        exit 1
    fi
    set -a
    # shellcheck source=/dev/null
    . "$env_file"
    set +a
    runbooks
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    render "$tmp"
    mimirtool rules sync --address="$GRAFANA_PROMETHEUS_URL" --id="$GRAFANA_PROMETHEUS_USER" \
        --key="$GRAFANA_RULES_TOKEN" "$here"/rules/*.yaml
    (cd "$tmp" && mimirtool alertmanager load --address="$GRAFANA_ALERTMANAGER_URL" \
        --id="$GRAFANA_ALERTMANAGER_USER" --key="$GRAFANA_RULES_TOKEN" alertmanager.yaml telegram.tmpl)
    dashboards_ok
    for f in "$here"/dashboards/*.json; do
        jq '{dashboard: (. + {id: null}), overwrite: true, message: "platform/observe/push.sh"}' "$f" |
            curl -fsS -X POST -H "Authorization: Bearer $GRAFANA_DASHBOARDS_TOKEN" -H "Content-Type: application/json" \
                --data-binary @- "${GRAFANA_URL%/}/api/dashboards/db" >/dev/null
        echo "dashboard $(jq -r .title "$f") uploaded"
    done
    echo "pushed: rules, Alertmanager config and dashboards are live"
    ;;
*)
    echo "usage: $0 check|push" >&2
    exit 2
    ;;
esac
