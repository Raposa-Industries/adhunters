#!/usr/bin/env bash
# Fails when a Go module imports another service's module.
#
# Every module may import kit (shared plumbing) and contract (the data
# contract). kit imports nothing from this repo. Anything else crossing
# folders is a wall breach: talk through the database's _api views instead.
set -euo pipefail
cd "$(dirname "$0")/.."

repo=github.com/marcosCapistrano/adhunters
fail=0
while read -r mod dir; do
  allowed="^($mod|$repo/kit|$repo/contract)(/|\$)"
  [[ $mod == "$repo/kit" ]] && allowed="^$repo/kit(/|\$)"
  bad=$(cd "$dir" && go list -deps -f '{{.ImportPath}}' ./... | grep "^$repo/" | grep -Ev "$allowed" || true)
  if [[ -n $bad ]]; then
    echo "wall breach in $mod:"; echo "$bad" | sed 's/^/  /'; fail=1
  fi
done < <(go list -m -f '{{.Path}} {{.Dir}}')

[[ $fail == 0 ]] && echo "walls ok: no module imports another service's code"
exit $fail
