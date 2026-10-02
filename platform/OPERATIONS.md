# Operating the platform

How the servers are laid out, how to get onto them, and how to build and
deploy. Keep this page true: a change to servers, access or deploys updates
it in the same PR.

## What exists

| Box | Hetzner name | Type | Location | Private IP | Public IPv4 | Runs |
|---|---|---|---|---|---|---|
| worker | `adhunters-worker` | CX43 | Nuremberg (nbg1) | 10.20.1.10 | 2.28.193.220 (Primary IP, kept across rebuilds) | capture `@a` `@b`, shipper, Raposa |
| data | `adhunters-data` | CX43 | Nuremberg (nbg1) | 10.20.1.20 | changes on rebuild; nothing listens on it | Postgres 17, loader, observe-bot, backups, create-web, create, library, Desk, launch-web, funnels-edge, funnels-loader, funnels-web |
| standby | `adhunters-standby` | CX23 | Falkenstein (fsn1) | 10.20.1.30 | 2.28.138.34 (Primary IP) | capture `@standby`, shipper |

All three are in one Hetzner project, created by Terraform
(`platform/terraform`). The firewall has no open public TCP port: everything
goes through Tailscale. Each box's role is in `/etc/adhunters/role`.

Object Storage, same Hetzner project, Falkenstein, private, endpoint
`fsn1.your-objectstorage.com`. One S3 key pair covers all of them.

| Bucket | Holds |
|---|---|
| `adhunters-raw` | every raw file capture wrote (Tracks' archive), and under `funnels/` every beacon funnels-edge received |
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
  -o bin/ ./tracks/cmd/... ./raposa/cmd/... ./create/cmd/... ./library/cmd/... ./desk/cmd/... ./intel/cmd/... ./launch/cmd/... ./spy/cmd/... ./funnels/cmd/... ./platform/observe/cmd/...
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

## The team's address, hunt-teste.fyi

`create-web` listens only on `127.0.0.1:8091` on the data box, and every
request to hunt-teste.fyi reaches it through a Cloudflare Tunnel
(`cloudflared` on the data box dials out to Cloudflare, so no port opens).
It asks for the team's sign-in (one username and password, a 30-day cookie;
create/README.md), then sends each request to the app whose path it starts
with (`web.Apps` in create/web: `/launch`, `/create`, `/intel`, `/spy`,
`/funnels`, `/desk`, `/raposa`) and every other path to `/launch/`. The
sign-in is what stops strangers spending OpenAI credit or touching Taboola:
the apps themselves listen only on localhost (raposa-web on the worker's
private address, `10.20.1.10:8090`, with `RAPOSA_WEB_BASE=/raposa`) and
trust whoever create-web lets through. The sign-in replaced Cloudflare Access on 2026-10-01 (owner's
word); there is no Access application any more.

Set up once, in the Cloudflare dashboard (Zero Trust):

1. Networks › Tunnels › Create a tunnel (Cloudflared), named
   `adhunters-data`. Copy the token it shows.
2. On the data box, install cloudflared and run it as a service with that
   token (`cloudflared service install TOKEN`, see create/README.md).
3. In the tunnel, one Public hostname: `hunt-teste.fyi`, no path, service
   `http://localhost:8091`. No other hostname rule for hunt-teste.fyi: a
   path rule straight to an app would skip the sign-in.

The sign-in lives in `/etc/adhunters/create-web.env`: `SIGNIN_USER=` and
`SIGNIN_PASSWORD_HASH=` (only the hash, never the password). create-web
refuses to start without them. To set or change the password (it signs
everyone out), with the password typed hidden:

```
sudo sed -i '/^SIGNIN_PASSWORD_HASH=/d' /etc/adhunters/create-web.env
read -rsp 'Password: ' P; echo; printf '%s' "$P" | /opt/adhunters/bin/create-web hash-password | sudo tee -a /etc/adhunters/create-web.env >/dev/null; unset P
sudo systemctl restart create-web
```

`/etc/adhunters/create-web.env` still holds the OpenAI and Taboola keys the
old page used; create-web no longer reads them (Launch's own file has the
Taboola ones). Leave the file as it is until the owner says to clean it up.

## Landing sites (Funnels)

`funnels-edge` listens only on `127.0.0.1:8098` on the data box and serves
every hosted landing site, the page script (`/ah.js`) and the collector
(`/e`). Visitors reach it through the same Cloudflare tunnel as
hunt-teste.fyi, straight to `localhost:8098` with no sign-in: landing pages
are public. Because the tunnel is
the only way in, the edge trusts Cloudflare's address and country headers
(`FUNNELS_TRUST_CLOUDFLARE=1`).

For each landing domain, once:

1. The domain's DNS is on Cloudflare (Websites › Add a site).
2. In the `adhunters-data` tunnel, Public hostname: the domain (and `www.`
   if wanted), service `http://localhost:8098`.
3. On the data box, publish its pages:
   `sudo -u funnels /opt/adhunters/bin/funnels-edge publish -site DOMAIN ./folder`.
4. Clarity (optional): make a project at clarity.microsoft.com for the
   domain, then `sudo -u funnels /opt/adhunters/bin/funnels-edge site -site DOMAIN -clarity PROJECT_ID`.

Beacons land in `/var/lib/funnels/spool` even while Postgres is down;
`funnels-loader` archives them to `adhunters-raw/funnels/` with the same keys
as Tracks' loader (setup.sh copies them). See funnels/README.md.
## Create on hunt-teste.fyi/create/

`create` listens only on `127.0.0.1:8095` on the data box (create/README.md).
create-web sends `/create` to it, behind the sign-in (above). Its OpenAI key goes in `/etc/adhunters/create.env`
(`OPENAI_API_KEY=`), then `sudo systemctl restart create`.

## The library and Google Drive

`library` listens only on `127.0.0.1:8093` on the data box; Create and Launch
call it there (library/README.md). Its files live in the team's Drive folder
only, with no bucket (decision 0020). The `adhunters-library` bucket held
them for a few hours on 1 Oct 2026 and is unused since; deleting it is the
owner's call.

The Drive copy needs, once: the Google client id and secret of the
`adhunters-library` project in the same file, `sudo systemctl restart
library`, then `sudo /opt/adhunters/bin/library drive-login` (it prints a
link to open and asks for the address the browser ends on). Without it the
library works and every creative waits to be copied.

## Desk on hunt-teste.fyi/desk/

`desk-web` listens only on `127.0.0.1:8092` on the data box
(desk/README.md). create-web sends `/desk` to it, behind the sign-in
(above). Desk is off (desk-agent runs but takes no work, and every page of
desk-web says to sign in) until its Claude key is in
`/etc/adhunters/desk-agent.env` (`ANTHROPIC_API_KEY=`) and the owner says so.
desk-web learned who is asking from Cloudflare Access's token, which is gone
since 2026-10-01: before Desk goes on, it needs to take that from the
sign-in instead. Its daily Claude limit
and stop switch are on its settings page.

## Intel on hunt-teste.fyi/intel/

`intel-web` listens only on `127.0.0.1:8096` on the data box
(intel/README.md). create-web sends `/intel` to it, behind the sign-in
(above). intel-collect reads once its keys are in
`/etc/adhunters/intel-collect.env` (the ZoltaGroup Taboola client id and
secret, and a RedTrack key), then `sudo systemctl restart intel-collect`.
Alerts reach Telegram once `/etc/adhunters/intel-numbers.env` has the same
`TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` as `observe-bot.env`, then
`sudo systemctl restart intel-numbers`.

## Spy on hunt-teste.fyi/spy/

`spy-web` listens only on `127.0.0.1:8097` on the data box, metrics on
`127.0.0.1:9116` (spy/README.md). create-web sends `/spy` to it, behind the
sign-in (above). `/etc/adhunters/spy-web.env` has no `ACCESS_TEAM` or
`ACCESS_AUD` since Cloudflare Access is gone: with them, spy-web refuses
every request.

## Funnels on hunt-teste.fyi/funnels/

`funnels-web` listens only on `127.0.0.1:8099` on the data box
(funnels/README.md). create-web sends `/funnels` to it, behind the sign-in
(above). It needs nothing else: setup.sh makes its
read-only `funnels_web` login. The landing domains stay separate and public
(above).

## Launch's page

`launch-web` (launch/README.md) listens only on `127.0.0.1:8094` on the data
box, metrics on `127.0.0.1:9111`. `setup.sh data` installs it, makes the
`launch` login and the `launch_api_read` role, and writes
`/etc/adhunters/launch-web.env` with the database line filled in (the password
is shown only on the run that makes the login). It starts without Taboola
keys: the pages work and the tree says Taboola is not connected.

create-web sends `/launch` to it, behind the sign-in (above). Launch is the only app that writes to Taboola and makes
everything paused. Its keys are the team's live ZoltaGroup login, the same
ones create-web holds (owner, 2026-10-01), without `TABOOLA_ONLY_OWN` so the
team can manage the campaigns already there. To copy them:
`sudo grep -E '^TABOOLA_(CLIENT_ID|CLIENT_SECRET|ACCOUNTS)=' /etc/adhunters/create-web.env | sudo tee -a /etc/adhunters/launch-web.env`,
then `sudo systemctl restart launch-web` (a later line in an env file wins).

## Not yet

- Deploys from CI (build, migrate, copy over Tailscale, wait for healthy).
  Until then, deploys are the commands above, run by hand.
- A home for secrets (sops or similar): the `.env` files are written by hand.
- The switch-over from the old collector (prodbox and bigworker keep running
  until the owner says otherwise). The plan and its commands are in
  [SWITCH-OVER.md](SWITCH-OVER.md); until the owner runs it, capture stays
  off on the new boxes and `tracks-bridge` is installed but not started.
