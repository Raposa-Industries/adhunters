# RenewalDue

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/credits.yaml`.

## What it means

A subscription in `credits.conf` renews, or must be paid, within its reminder window (`remind`, 7 days by default). Some of the services the platform needs have no balance API: the datacenter and ISP proxy lines, for example. For those, the renewal date is the only warning we get before they stop.

## Check

1. The alert names the renewal and how long is left.
2. At the provider: does it renew by itself? Is the card on file valid, or is the prepaid balance behind it enough?

## Fix

- Renew or pay. Payments are the owner's.
- Plan changed (a new date, a different period): edit the section in `/etc/adhunters/credits.conf`, then `systemctl restart observe-bot`. `observe-bot credits` prints the next date to check it.
- The subscription ended for good: remove its section.

## After

A monthly, weekly or yearly renewal moves to its next date the day after it is due, and the alert clears. A one-off renewal (`every = none`) keeps firing, as overdue, until its date is changed or its section removed.
