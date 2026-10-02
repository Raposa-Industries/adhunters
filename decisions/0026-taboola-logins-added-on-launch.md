# 0026 · Taboola logins added on Launch's Contas page

**Decided:** 2 Oct 2026, by Claude, under the owner's ask for "a new account
page ... where the user will be able to put his credentials so we can load
more accounts" (2 Oct 2026). No drawing of the page existed yet, so the
default below was picked and said so in the thread.

Until now Launch used one Taboola login, the server's own (`TABOOLA_*` in
`/etc/adhunters/launch-web.env`), and only the owner could change it, on the
server. Now people add more Taboola logins on Launch's Contas page
(`/launch/accounts`), and choose which of each login's advertiser accounts
Launch uses.

- **Only reads at Taboola.** Checking a login asks for a token and the
  login's account list (allowed-accounts); nothing is made or changed.
  Every call about an account then goes to its own login, through the same
  write client and guards (never a network account, the ceilings, nothing
  made running).
- **The secret is sealed.** It is stored in `launch.login` sealed with
  AES-256-GCM, tied to its network and client ID, with a 32-byte key in a
  file on the data box (`/var/lib/launch-web/login.key`, made by Launch,
  owner-only), never in the database. No page, answer, log line or kept
  exchange holds it. A page sees only the client ID's first and last four
  characters.
- **Launch owns it.** Launch is the one app that writes to Taboola
  (launch/README.md), so the logins live in its schema. Intel
  still reads only its own env logins; giving it the added ones is a later
  change through `contract/`.

**Why a key file and not an env setting:** it needs no step from the owner
before the deploy, and a copy of the database alone (backups included)
cannot open a secret. Losing the file loses only the added logins, which
are added again; the server's own login does not depend on it.
