# Runbooks

One file per alert, named after it (`CaptureStopped` is `capture-stopped.md`).
Every alert's Telegram message links its runbook. Each one says what the alert
means, what to check, how to fix it, and what happens after.

They are written so a person or an agent can follow them. An agent may run
the checks (read logs, metrics, `status` commands) and propose the fix; a
restart, a rollback or anything that changes a box waits for the owner's word
(AGENTS.md: never change production without it).

`platform/observe/push.sh check` fails when an alert has no runbook here, so
a new alert comes with its runbook in the same PR.

| Tier | Alerts |
|---|---|
| Page | [CaptureStopped](capture-stopped.md), [CaptureLow](capture-low.md), [BoxGone](box-gone.md), [DiskFullSoon](disk-full-soon.md), [PostgresDown](postgres-down.md), [WalArchiveFailing](wal-archive-failing.md), [BackupLate](backup-late.md), [CreditRunningOut](credit-low.md), [OutOfCredit](out-of-credit.md), [FunnelsSpoolFailing](funnels-spool-failing.md) |
| Chat | [CaptureInstanceQuiet](capture-instance-quiet.md), [CaptureErrors](capture-errors.md), [PublisherQuiet](publisher-quiet.md), [ShipperBehind](shipper-behind.md), [LoaderLag](loader-lag.md), [RawFileQuarantined](raw-file-quarantined.md), [BridgeBehind](bridge-behind.md), [BridgeFileQuarantined](bridge-behind.md), [FormatDrift](format-drift.md), [HourNotClosed](hour-not-closed.md), [WalkerStopped](walker-stopped.md), [WalkerFailing](walker-failing.md), [WalkerBehind](walker-behind.md), [TaskLate](task-late.md), [TaskFailing](task-failing.md), [ErrorsLogged](errors-logged.md), [ServiceDown](service-down.md), [ServiceRestarting](service-restarting.md), [RaposaBrowserDown](raposa-browser-down.md), [RaposaIdle](raposa-idle.md), [RaposaQueueEmpty](raposa-queue-empty.md), [WatchDeliveryFailing](watch-delivery-failing.md), [DiskFilling](disk-filling.md), [MemoryLow](memory-low.md), [OutOfMemoryKill](out-of-memory-kill.md), [UnitFailed](unit-failed.md), [PostgresRestarted](postgres-restarted.md), [PostgresConnections](postgres-connections.md), [BackupRepoUnreadable](backup-late.md), [CreditLow](credit-low.md), [RenewalDue](renewal-due.md), [FunnelsFileQuarantined](funnels-file-quarantined.md), [FunnelsBeaconsUnparsed](funnels-beacons-unparsed.md) |
| Heartbeat | [Heartbeat](heartbeat.md) |
