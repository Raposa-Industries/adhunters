# TunnelDown

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/web.yaml`.

## What it means

cloudflared on the data box has had no connection to Cloudflare for 5 minutes, or is not running. Every app at hunt-teste.fyi is out of reach: requests stop at Cloudflare. Collection does not go through it and carries on.

## Check

1. On the data box: `systemctl status cloudflared` and `journalctl -u cloudflared -n 50`.
2. Running but not connected: `curl -s 127.0.0.1:20241/metrics | grep ha_connections` (4 when healthy), and whether the box reaches the internet at all (`curl -sI https://www.cloudflare.com`).
3. In Cloudflare's dashboard, Networks › Tunnels: the tunnel's status.

## Fix

- Stopped or stuck: `systemctl restart cloudflared`.
- The box cannot reach the internet: Hetzner's status page, then the box's network.
- The tunnel was deleted or its token changed in Cloudflare: install it again with the new token (`cloudflared service install TOKEN`, `create/README.md`).

## After

It clears once cloudflared holds a connection to Cloudflare again.
