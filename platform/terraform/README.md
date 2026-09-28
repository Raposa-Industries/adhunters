# Terraform

Creates the Hetzner side of servers A+ (decision 0002): the worker box, the
data box, the standby capture box, Primary IPs for the two capture boxes, a
private network and a firewall with no public TCP ports. The Cloudflare
tunnel, DNS and Access apps are added when the first new app goes up.

State and runs live in HCP Terraform (organization `adhunters`, workspace
`platform`). Set these as workspace variables, marked sensitive where needed:
`hcloud_token`, `tailscale_auth_key`, `admin_ssh_keys`, and later
`cloudflare_api_token`.

**Nothing has been applied.** An apply creates servers that cost money, and
the proxy provider must allowlist the `capture_ips` output before capture runs
from them.

Checks without credentials: `terraform fmt -check && terraform init -backend=false && terraform validate`.
