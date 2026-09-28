# BoxGone

Page: Telegram, with sound, every 5 minutes until it clears. Rule: `platform/observe/rules/`.

## What it means

A box that was sending metrics has sent none for over 5 minutes. It is down, cut off from the internet, or its Alloy stopped.

## Check

1. Hetzner console: is the server running? Its graphs show whether it is alive.
2. `tailscale ping adhunters-<box>` and `ssh admin@adhunters-<box>`.
3. If it answers: `systemctl status alloy` and `journalctl -u alloy -n 50`. Only the metrics are missing then; check that the services themselves run.

## Fix

- Alloy stopped: `systemctl restart alloy`.
- The box hangs: restart it from the Hetzner console.
- The box is dead: replace it (`platform/terraform`, then `platform/servers/setup.sh`). The capture IPs are Primary IPs and move to the new box. If it is the worker box, raise the standby to 8 workers first (see [capture-stopped](capture-stopped.md)).
- The data box is dead: raw files keep arriving in the spools on the capture boxes and wait there; nothing is lost while it is replaced.

## After

It clears when the box sends metrics again.
