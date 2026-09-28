# 0005 · Hosted, established tools

**Decided:** 28 Sep 2026, by Marcos.

Grafana Cloud (with Alloy on each box) for metrics, logs and alerts; Sentry
for errors; Better Stack for outside checks and the dead-man heartbeat;
Terraform with HCP Terraform rather than OpenTofu; Blacksmith runners for CI.
Cloudflare Access goes in front of apps when the new apps launch, not before;
Tailscale carries machines and SSH.

**Why:** the owner wants to spend time on features, not on running tools.
