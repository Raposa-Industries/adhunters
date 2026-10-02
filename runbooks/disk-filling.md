# DiskFilling

Chat: Telegram "AdHunters alerts", silent, at any hour. Rule: `platform/observe/rules/`.

## What it means

A disk on this box has less than 15% free. Not urgent yet; **DiskFullSoon** pages if it is racing.

## Check

The same checks as [disk-full-soon](disk-full-soon.md): find what grows with `du`, and whether it is the spool, Postgres or logs.

## Fix

The same fixes as [disk-full-soon](disk-full-soon.md). Plan a resize if the growth is expected.

## After

It clears above 15% free.
