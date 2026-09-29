# OutOfCredit

Page: Telegram, with sound, every 5 minutes until it clears. Rule: `platform/observe/rules/credits.yaml`.

## What it means

A paid service refused one of our services because the account is out of credit or quota. Examples are an HTTP 402, OpenAI's `insufficient_quota`, Anthropic's "credit balance is too low", and a proxy refusing for lack of traffic. The service counts each refusal through `kit/ops` (`OutOfCredit`). The work that needs the provider has stopped. This is the alarm for services whose balance cannot be read ahead of time.

## Check

1. The alert names the `provider` and the `service` that was refused.
2. The provider's billing page: the balance, the plan's quota, and whether a payment failed.
3. `journalctl -u <service> --since -30min`: the refusal's own message (it is also in Sentry).

## Fix

- Top up or raise the quota at the provider. Payments are the owner's.
- If the provider has an auto-recharge or low-balance email, turn it on. If it has a balance API, add it to `credits.conf` so the next time warns ahead (see `platform/observe/README.md`, "Credits").

## After

It clears 10 minutes after the last refusal. Work retried after the top-up needs no action; anything that gave up is in the service's own runbook.
