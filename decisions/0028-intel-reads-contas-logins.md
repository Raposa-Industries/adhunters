# 0028 · Intel reads the Taboola logins added on Contas

**Decided:** 2 Oct 2026, by Claude, under the owner's ask to "make sure the
notifications for delivery status changes are working on telegram". The
logins added on Launch's Contas page (decision 0026) were never read by
Intel, so their campaigns had no delivery status messages, alerts or
numbers.

- **Through `contract/`.** Launch publishes the added logins in
  `launch_api.taboola_login_v1`, the secret and the proxy still sealed. Only
  the `intel` login may read the view; `launch_api_read` (Create, Desk) may
  not.
- **The same key, kept apart from the database.** intel-collect opens the
  rows with `LAUNCH_LOGIN_KEY_BASE64`, the same value as in
  `launch-web.env`, in `/etc/adhunters/intel-collect.env`. Without it,
  nothing changes: Intel reads only its own env logins. The sealing and the
  proxy transport moved to `shared/taboola/logins`, so Launch and Intel open
  them the same way.
- **Through the login's proxy, never direct.** Every request for those
  accounts, the token included, goes through the login's proxy; a login with
  no proxy, or one that does not open, is not read at all.
- **Still read-only.** The secret can write at Taboola, as the env logins'
  can; Intel's client refuses anything but GETs and the token request.
- **Only the chosen accounts, once.** Intel keeps to the accounts chosen for
  the login on Contas, and leaves out any its own env logins already read.
- **Changes are picked up within 5 minutes.** intel-collect reads the view
  at start and every 5 minutes; when a login is added, removed or changed it
  ends cleanly and systemd starts it again with the new set.

**Not covered:** the server's own login's accounts that have a proxy on
Contas (`launch.account_proxy`) are still read direct by Intel's env login,
as before.
