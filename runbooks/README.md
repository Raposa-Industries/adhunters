# Runbooks

One file per alert, named after it (`CaptureStopped` is `capture-stopped.md`).
Every alert's Telegram message links its runbook. Each one says what the alert
means, what to check, how to fix it, and what happens after.

They are written so a person or an agent can follow them. An agent may run
the checks (read logs, metrics, `status` commands) and propose the fix; a
restart, a rollback or anything that changes a box waits for the owner's word
(CLAUDE.md: never change production without it).

`platform/observe/push.sh check` fails when an alert has no runbook here, so
a new alert comes with its runbook in the same PR.

| Tier | Alerts |
|---|---|
| Page | [CaptureStopped](capture-stopped.md), [CaptureLow](capture-low.md), [BoxGone](box-gone.md), [DiskFullSoon](disk-full-soon.md), [PostgresDown](postgres-down.md), [WalArchiveFailing](wal-archive-failing.md), [BackupLate](backup-late.md) |
| Chat | [CaptureInstanceQuiet](capture-instance-quiet.md), [CaptureErrors](capture-errors.md), [PublisherQuiet](publisher-quiet.md), [ShipperBehind](shipper-behind.md), [LoaderLag](loader-lag.md), [RawFileQuarantined](raw-file-quarantined.md), [FormatDrift](format-drift.md), [HourNotClosed](hour-not-closed.md), [TaskLate](task-late.md), [ServiceDown](service-down.md), [ServiceRestarting](service-restarting.md), [RaposaBrowserDown](raposa-browser-down.md), [WatchDeliveryFailing](watch-delivery-failing.md), [DiskFilling](disk-filling.md), [MemoryLow](memory-low.md), [OutOfMemoryKill](out-of-memory-kill.md), [UnitFailed](unit-failed.md), [PostgresConnections](postgres-connections.md), [BackupRepoUnreadable](backup-late.md) |
| Heartbeat | [Heartbeat](heartbeat.md) |
