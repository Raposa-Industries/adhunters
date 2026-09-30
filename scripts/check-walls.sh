#!/usr/bin/env bash
# Fails when a Go module imports another service's module.
#
# Every module may import kit (shared plumbing), contract (the data
# contract) and shared (code several services need, decision 0013). kit
# imports nothing from this repo; shared imports only kit. Anything else
# crossing folders is a wall breach: move the code to shared, or talk through
# the database's _api views.
set -euo pipefail
cd "$(dirname "$0")/.."

repo=github.com/Raposa-Industries/adhunters
fail=0
while read -r mod dir; do
  allowed="^($mod|$repo/kit|$repo/contract|$repo/shared)(/|\$)"
  [[ $mod == "$repo/kit" ]] && allowed="^$repo/kit(/|\$)"
  [[ $mod == "$repo/shared" ]] && allowed="^($repo/shared|$repo/kit)(/|\$)"
  bad=$(cd "$dir" && go list -deps -f '{{.ImportPath}}' ./... | grep "^$repo/" | grep -Ev "$allowed" || true)
  if [[ -n $bad ]]; then
    echo "wall breach in $mod:"; echo "$bad" | sed 's/^/  /'; fail=1
  fi
done < <(go list -m -f '{{.Path}} {{.Dir}}')

# Only Launch writes to Taboola; create-web's launcher page too, until Launch
# replaces it. Every other module reads Taboola through shared/taboola.
write=$repo/shared/taboola/write
while read -r mod dir; do
  case $mod in $repo/launch | $repo/create | $repo/shared) continue ;; esac
  if (cd "$dir" && go list -deps -f '{{.ImportPath}}' ./... | grep -qx "$write"); then
    echo "wall breach in $mod: only launch (and create-web, until Launch replaces it) may import $write"; fail=1
  fi
done < <(go list -m -f '{{.Path}} {{.Dir}}')

[[ $fail == 0 ]] && echo "walls ok: no module imports another service's code, and only launch writes to Taboola"
exit $fail
