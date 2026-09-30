# Operating the platform

How the servers are laid out, how to get onto them, and how to build and
deploy. Keep this page true: a change to servers, access or deploys updates
it in the same PR.

## What exists

| Box | Hetzner name | Type | Location | Private IP | Public IPv4 | Runs |
|---|---|---|---|---|---|---|
| worker | `adhunters-worker` | CX43 | Nuremberg (nbg1) | 10.20.1.10 | 2.28.193.220 (Primary IP, kept across rebuilds) | capture `@a` `@b`, shipper, Raposa |
| data | `adhunters-data` | CX43 | Nuremberg (nbg1) | 10.20.1.20 | changes on rebuild; nothing listens on it | Postgres 17, loader, observe-bot, backups, create-web, create, library, Desk |
| standby | `adhunters-standby` | CX23 | Falkenstein (fsn1) | 10.20.1.30 | 2.28.138.34 (Primary IP) | capture `@standby`, shipper |

All three are in one Hetzner project, created by Terraform
(`platform/terraform`). The firewall has no open public TCP port: everything
goes through Tailscale. Each box's role is in `/etc/adhunters/role`.

Object Storage, same Hetzner project, Falkenstein, private, endpoint
`fsn1.your-objectstorage.com`. One S3 key pair covers all of them.

| Bucket | Holds |
|---|---|
| `adhunters-raw` | every raw file capture wrote (Tracks' archive) |
| `adhunters-raposa` | Raposa's captured files |
| `adhunters-backups` | Postgres WAL and base backups (pgBackRest) |

Proxies log in with username and password; no provider has an IP allowlist
for them (checked 28 Sep 2026), so the new boxes need no allowlist change.

State on 29 Sep 2026: setup.sh has run on all three boxes. Postgres, the
loader, backups, both shippers, Raposa's engine and web run; capture is
stopped on the worker and not configured on the standby, because the proxy
lines are shared with the old collector (bigworker), and today's Spy reads
only the old collector's database on prodbox. Grafana Alloy and observe-bot
wait for their accounts (`FILL_ME`).

## Accounts

| What | Where | Notes |
|---|---|---|
| Servers, buckets | Hetzner Console, the adhunters project | New accounts have a shared-vCPU limit; it was raised to 30 on 28 Sep 2026. Ask under Limits before adding servers. |
| Terraform state and runs | app.terraform.io, organization `adhunters`, workspace `platform` | Workspace variables: `hcloud_token`, `tailscale_auth_key`, `admin_ssh_keys`. **Terraform Working Directory must be `platform/terraform`**, or runs can't see `platform/servers/`. |
| Machine access | login.tailscale.com | Boxes join tagged `tag:server` with the reusable auth key. |
| Metrics, logs, alerts | Grafana Cloud, Sentry, Better Stack, Telegram | See `platform/observe/README.md`. |

Keys and passwords live in the owner's password manager and in the boxes'
`/etc/adhunters/*.env` files (root and the service user only). Never in the
repo, the project chat or WhatsApp.

## Getting onto a box

Tailscale on your computer, signed in to the same tailnet, then:

```
ssh admin@adhunters-data      # or adhunters-worker, adhunters-standby
```

Boxes use Tailscale SSH, so the tailnet policy (Access controls) needs this
rule inside `"ssh": [ ]`, beside the default one:

```
{
    "action": "accept",
    "src":    ["autogroup:member"],
    "dst":    ["tag:server"],
    "users":  ["admin", "root"],
},
```

Without it SSH fails with "tailnet policy does not permit you to SSH to this
node". Run commands one at a time: lines pasted together with `ssh` run on
your own computer after it.

Useful on a box:

```
systemctl status 'tracks-*' 'raposa-*'     # what runs
journalctl -u tracks-loader -f             # follow one service's log
curl -s localhost:9104/healthz             # a service's health (ports in platform/servers/README.md)
sudo -u tracks /opt/adhunters/bin/tracks-loader status -books   # needs its env: see below
```

For a command that needs a service's settings:
`sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; /opt/adhunters/bin/tracks-loader status'`.

## Servers: create or change

From a checkout of `main`:

```
cd platform/terraform
terraform login      # once; a 30-day token is fine
terraform plan       # read it: expect 0 to change, 0 to destroy unless you meant it
terraform apply
```

The run happens in HCP Terraform (also startable from the website: workspace,
New run, Plan and apply). A partial apply is safe to repeat: it only creates
what is missing.

## Build and deploy

Build on your computer (Go 1.25), from the repository root:

```
git checkout main && git pull
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(git rev-parse --short HEAD)" \
  -o bin/ ./tracks/cmd/... ./raposa/cmd/... ./create/cmd/... ./library/cmd/... ./desk/cmd/... ./intel/cmd/... ./platform/observe/cmd/...
```

Copy the checkout with the binaries to a box and run the setup there:

```
rsync -az --exclude .git ./ admin@adhunters-data:adhunters/
ssh admin@adhunters-data
cd ~/adhunters/platform/servers
sudo ./setup.sh --bin ~/adhunters/bin
```

Order: **data box first** (it creates the database logins and prints their
passwords once), then worker, then standby. The setup can run again at any
time; that is also how a new build is deployed. It keeps the previous build
as `NAME.prev` and restarts only units whose settings are complete; the
capture instances restart one at a time, so collection never stops. It ends
with a list of what is still to fill in. Details: `platform/servers/README.md`.

The worker and standby boxes share some settings with the data box: the
object storage keys and the database logins `tracks_shipper` and `raposa`.
Once the data box has its keys in `tracks-loader.env` and setup.sh has run on
the other box, fill them in from your computer, with nothing secret on screen:

```
platform/servers/share-secrets.sh            # or: share-secrets.sh worker
```

It copies the keys from the data box, copies a login's password from a box
that already has it (the first time, it sets a new one on the data box),
writes them into the `.env` files and restarts what is ready. It can run
again at any time.

If a setup stops at `sudo: Account or password is expired` and asks for
root's current password, press Ctrl+C (nothing is lost) and run
`sudo chage -d "$(date +%F)" -M -1 root`, then the setup again. Hetzner
expires root's random password at creation; the setup now clears it itself.

If the worker or standby can't reach the database (`pg: ping: context
deadline exceeded`, `ping 10.20.1.20` gets no answer), check `ip -brief addr`
on it: `enp7s0 DOWN` means Ubuntu left the private network card off. The
setup now switches it on (`/etc/netplan/60-private.yaml`, DHCP); by hand, write
that file and run `sudo netplan apply`.

Rolling back one binary on a box:
`sudo cp /opt/adhunters/bin/NAME.prev /opt/adhunters/bin/NAME && sudo systemctl restart UNIT`.

## Create's page on hunt-teste.fyi

`create-web` listens only on `127.0.0.1:8091` on the data box. People reach
it at https://hunt-teste.fyi through a Cloudflare Tunnel (`cloudflared` on
the data box dials out to Cloudflare, so no port opens), behind Cloudflare
Access, which asks for an allowed email before the page loads. Access is
what stops strangers spending OpenAI credit: never publish the hostname
without it.

Set up once, in the Cloudflare dashboard (Zero Trust):

1. Networks › Tunnels › Create a tunnel (Cloudflared), named
   `adhunters-data`. Copy the token it shows.
2. On the data box, install cloudflared and run it as a service with that
   token (`cloudflared service install TOKEN`, see create/README.md).
3. In the tunnel, Public hostname: `hunt-teste.fyi`, service
   `http://localhost:8091`.
4. Access › Applications › Add (Self-hosted) for `hunt-teste.fyi`, with a
   policy that allows the team's emails (One-time PIN login).

The OpenAI key goes in `/etc/adhunters/create-web.env`
(`OPENAI_API_KEY=`), then `sudo systemctl restart create-web`.

The Taboola keys go in the same file (`TABOOLA_CLIENT_ID`,
`TABOOLA_CLIENT_SECRET`, `TABOOLA_ACCOUNTS`; the full list is in
create/README.md). The only keys we hold are the lent ZoltaGroup login's,
allowed for paused tests only, so with them the file must also carry
`TABOOLA_ONLY_OWN=1`, and what the test made (the ids in
`/var/lib/create-web/kept/taboola-state.json`) is deleted afterwards. Without Taboola keys the page still makes the
bulk sheet.

## Create on hunt-teste.fyi/create/

`create` listens only on `127.0.0.1:8095` on the data box (create/README.md).
It is reached on the same address and behind the same Access application as
the launcher page: in the tunnel (Networks › Tunnels › `adhunters-data` ›
Public hostname), add a hostname `hunt-teste.fyi` with path `^/create`,
service `http://localhost:8095`, and move it above the one that serves the
whole hostname. Its OpenAI key goes in `/etc/adhunters/create.env`
(`OPENAI_API_KEY=`), then `sudo systemctl restart create`.

## The library and Google Drive

`library` listens only on `127.0.0.1:8093` on the data box; Create and Launch
call it there (library/README.md). Its safe copies go in their own bucket:
create `adhunters-library` in the Hetzner Console (Object Storage, fsn1,
private) and put the object storage keys in `/etc/adhunters/library.env`.

The Drive copy needs, once: the Google client id and secret of the
`adhunters-library` project in the same file, `sudo systemctl restart
library`, then `sudo /opt/adhunters/bin/library drive-login` (it prints a
link to open and asks for the address the browser ends on). Without it the
library works and every creative waits to be copied.

## Desk on hunt-teste.fyi/desk/

`desk-web` listens only on `127.0.0.1:8092` on the data box
(desk/README.md). It is reached on the same address and behind the same
Access application as the launcher page: in the tunnel (Networks › Tunnels ›
`adhunters-data` › Public hostname), add a hostname `hunt-teste.fyi` with
path `^/desk`, service `http://localhost:8092`, and move it above the one
that serves the whole hostname. Desk answers once its Claude key is in
`/etc/adhunters/desk-agent.env` (`ANTHROPIC_API_KEY=`), then
`sudo systemctl restart desk-agent`. Its daily Claude limit and stop switch
are on its settings page.

## Intel on hunt-teste.fyi/intel/

`intel-web` listens only on `127.0.0.1:8096` on the data box
(intel/README.md). It is reached on the same address and behind the same
Access application as the launcher page: in the tunnel (Networks › Tunnels ›
`adhunters-data` › Public hostname), add a hostname `hunt-teste.fyi` with
path `^/intel`, service `http://localhost:8096`, and move it above the one
that serves the whole hostname. intel-collect reads once its keys are in
`/etc/adhunters/intel-collect.env` (the ZoltaGroup Taboola client id and
secret, and a RedTrack key), then `sudo systemctl restart intel-collect`.
Alerts reach Telegram once `/etc/adhunters/intel-numbers.env` has the same
`TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` as `observe-bot.env`, then
`sudo systemctl restart intel-numbers`.

## Not yet

- Deploys from CI (build, migrate, copy over Tailscale, wait for healthy).
  Until then, deploys are the commands above, run by hand.
- A home for secrets (sops or similar): the `.env` files are written by hand.
- The switch-over from the old collector (prodbox and bigworker keep running
  until the owner says otherwise). The plan and its commands are in
  [SWITCH-OVER.md](SWITCH-OVER.md); until the owner runs it, capture stays
  off on the new boxes and `tracks-bridge` is installed but not started.
