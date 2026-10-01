#!/usr/bin/env bash
# Copies the creatives the team kept in Auto-Creative (prodbox) into a folder
# on your computer, laid out the way the library reads the team's Drive
# folder: <vertical>/Auto-Creative/<name>.<ext>, each file keeping the name
# Auto-Creative gave it (MMT48, or its number). It only reads prodbox. Run it
# on your own computer (Tailscale up, `ssh prod` working), from the
# repository:
#
#   platform/retire/auto-creative-to-drive.sh [folder]   (default ~/auto-creative-for-drive)
#
# Then drag each vertical folder it made into the library folder in Google
# Drive (signed in as the library's account). The library's next Drive pass
# (5 minutes, or POST /api/drive/sync) adds every picture as a creative of
# that vertical, in a set called "Auto-Creative", keeping its file name. Where
# the vertical's folder already exists in Drive, open it and drag only the
# Auto-Creative folder inside, so the vertical keeps one folder.
#
# It also raises the library's counter for each vertical whose code
# Auto-Creative used, so names minted from now on never repeat one of these.
set -euo pipefail

out=${1:-$HOME/auto-creative-for-drive}
data=admin@adhunters-data
ssh_opts=(-o ConnectTimeout=15 -o BatchMode=yes)
port=19000
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"; [ -n "${tunnel:-}" ] && kill "$tunnel" 2>/dev/null || true' EXIT

echo "== reading Auto-Creative's pictures (kept creatives only)"
ac_psql() { ssh "${ssh_opts[@]}" prod "docker exec -i auto-creative-postgres-1 sh -c 'psql -X -q --csv -v ON_ERROR_STOP=1 -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\"'"; }
ac_psql > "$out/auto-creative-manifest.csv" <<'SQL'
SELECT i.id, i.object_key, i.content_type, i.width, i.height, i.exported_name, i.vertical_number,
       v.code, v.platform_letter, v.name AS vertical_name, array_to_string(v.synonyms, '|') AS synonyms,
       c.name AS conversation, i.prompt, i.created_at
FROM images i
LEFT JOIN verticals v ON v.id = i.vertical_id
LEFT JOIN conversations c ON c.id = i.conversation_id
WHERE i.kind = 'generated' AND i.kept
ORDER BY i.id;
SQL
ac_psql > "$work/ac-verticals.csv" <<'SQL'
SELECT code, platform_letter, name, array_to_string(synonyms, '|') AS synonyms, next_number FROM verticals ORDER BY code;
SQL
bucket=$(ssh "${ssh_opts[@]}" prod 'docker exec auto-creative-api-1 printenv S3_BUCKET' 2>/dev/null || true)
bucket=${bucket:-auto-creative}

echo "== the library's verticals (data box)"
ssh "${ssh_opts[@]}" "$data" "sudo -u postgres psql -X -q --csv -v ON_ERROR_STOP=1 -d adhunters" > "$work/lib-verticals.csv" <<'SQL'
SELECT id, name, code, network_letter, next_number, drive_folder_id IS NOT NULL AS in_drive FROM library.vertical ORDER BY name;
SQL

echo "== downloading through a tunnel to prodbox's MinIO (bucket $bucket)"
ssh "${ssh_opts[@]}" -N -o ExitOnForwardFailure=yes -L "$port:127.0.0.1:9000" prod &
tunnel=$!
sleep 3

python3 - "$out" "$work" "$bucket" "$port" <<'PY'
import csv, os, sys, urllib.request, urllib.parse, urllib.error, collections, json
out, work, bucket, port = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
rows = list(csv.DictReader(open(os.path.join(out, "auto-creative-manifest.csv"))))
lib = list(csv.DictReader(open(os.path.join(work, "lib-verticals.csv"))))
acv = list(csv.DictReader(open(os.path.join(work, "ac-verticals.csv"))))

by_code = {v["code"].upper(): v for v in lib if v["code"]}
by_word = {}
for v in lib:
    by_word[v["name"].lower()] = v
    by_word[v["id"].lower()] = v

def match(code, name, synonyms):
    if code and code.upper() in by_code:
        return by_code[code.upper()]
    for w in [name] + [s for s in synonyms.split("|") if s]:
        if w and w.lower() in by_word:
            return by_word[w.lower()]
    return None

ext = {"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif"}
counts, unmatched, failed, names = collections.Counter(), collections.Counter(), [], set()
for r in rows:
    if r["exported_name"]:
        name = r["exported_name"]
    elif r["code"] and r["vertical_number"]:
        name = r["code"] + r["platform_letter"] + r["vertical_number"]
    else:
        name = "%04d" % int(r["id"])
    v = match(r["code"], r["vertical_name"], r["synonyms"]) if r["vertical_name"] else None
    if v:
        top = v["name"]
    elif r["vertical_name"]:
        top = r["vertical_name"]
        unmatched[top] += 1
    else:
        top = "Sem vertical"
        unmatched[top] += 1
    folder = os.path.join(out, top, "Auto-Creative")
    os.makedirs(folder, exist_ok=True)
    fname = "%s.%s" % (name, ext.get(r["content_type"], "jpg"))
    if (top, fname) in names:
        fname = "%s-%s.%s" % (name, r["id"], ext.get(r["content_type"], "jpg"))
    names.add((top, fname))
    path = os.path.join(folder, fname)
    if os.path.exists(path) and os.path.getsize(path) > 0:
        counts[top] += 1
        continue
    url = "http://127.0.0.1:%s/%s/%s" % (port, bucket, urllib.parse.quote(r["object_key"]))
    try:
        with urllib.request.urlopen(url, timeout=60) as resp, open(path + ".part", "wb") as f:
            f.write(resp.read())
        os.rename(path + ".part", path)
        counts[top] += 1
    except (urllib.error.URLError, OSError) as e:
        failed.append((r["id"], r["object_key"], str(e)))

print("\nKept creatives: %d, saved: %d, failed: %d" % (len(rows), sum(counts.values()), len(failed)))
in_drive = {v["name"] for v in lib if v["in_drive"] == "t"}
for top, n in sorted(counts.items()):
    note = ""
    if top in in_drive:
        note = "  (already in Drive: drag only its Auto-Creative folder into it)"
    elif top in unmatched:
        note = "  (NOT one of the library's verticals: the library adds these with no vertical)"
    print("  %-28s %5d%s" % (top, n, note))
for f in failed[:20]:
    print("  failed: image %s (%s): %s" % f)

# Counters: for each library vertical whose code Auto-Creative used, the
# library's next number must be past Auto-Creative's.
bumps = {}
for a in acv:
    v = by_code.get(a["code"].upper())
    if v and v["network_letter"].upper() == a["platform_letter"].upper() and int(a["next_number"]) > int(v["next_number"]):
        bumps[v["id"]] = int(a["next_number"])
json.dump(bumps, open(os.path.join(work, "bumps.json"), "w"))
PY

if [ "$(cat "$work/bumps.json")" != "{}" ]; then
    echo "== raising the library's counters past Auto-Creative's: $(cat "$work/bumps.json")"
    python3 -c 'import json,sys; [print(k, v) for k, v in json.load(open(sys.argv[1])).items()]' "$work/bumps.json" |
    while read -r id n; do
        ssh "${ssh_opts[@]}" "$data" "curl -sf -X PATCH -H 'Content-Type: application/json' -d '{\"next_number\": $n}' http://127.0.0.1:8093/api/verticals/$id" </dev/null
        echo
    done
fi
echo
echo "Done: $out"
echo "Next: drag the folders above into https://drive.google.com/drive/folders/1ZDULRMNk-5JohCAI_ZG9W5lvvEp8HP3Q"
echo "Then: ssh $data 'curl -s -X POST http://127.0.0.1:8093/api/drive/sync; sleep 60; curl -s http://127.0.0.1:8093/api/status'"
