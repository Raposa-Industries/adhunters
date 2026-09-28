# 0002 · Servers A+ on Hetzner, as code

**Decided:** 28 Sep 2026, by Marcos.

Two CX43s in eu-central (worker box, data box) and a CX23 standby capture box
in a second eu-central datacenter. Capture IPs are Hetzner Primary IPs so the
proxy allowlist survives a server swap. Terraform creates them, with state in
HCP Terraform. Upgrade the data box to an AX42 only if Direction takes over a
minute, the cache hit rate falls under 99%, or data passes 80 GB.

**Why:** measured usage is small, costs stay predictable, and collection
survives a whole box failing.

**Before any apply:** measure raw bytes per scrape and hour-close and
Direction speed on a CX43, and get both capture IPs allowlisted.
