# Current task: 2026-10-28 intelligence cooldown fix — deployed

Production Codex2API on netcup 159.195.12.230 runs source
`e61b04c3b9a72cbd75066ec09f7f5be7bf7f75cb`, tree
`23d4b944ea0193fb07bc4b451ee5901954cf4e77`; authoritative record is
`/opt/codex2api/source-release.json`. Documentation commits after this source do
not require another deployment. Upstream `3c6adfda702efed67931b2d98a491d72bd9d91b5`
was fetched again after deployment and is already an ancestor.

Automatic checks defer during cooldown/quota exhaustion and resume when available.
Enqueue, claim, runtime and final HTTP/WebSocket dispatch enforce availability;
manual switches, frequency, enrollment and history remain intact. Manual probes
and intelligence-owned quality recovery retain their prior behavior.

The user ultimately requested direct downtime deployment. Initial maintenance was
2026-10-28 01:12:04.386–01:12:20.877 +08:00 (16.491 seconds). External concurrent
Sub2API deployment replaced three non-target containers during public smoke,
triggering protective maintenance at approximately 01:14:19.858. Recovery retained
the current database and current non-target routing, restarted the same candidate,
and reopened traffic at 01:17:24.560 (second window about 184.702 seconds).
Total maintenance about 201.192 seconds. No old dump was restored. Recovery public
smoke passed at 01:19:06; independent final verification passed at 01:22:05.

Detection: enabled=true, interval600, revision12; history103 retained. Account
switches and enrollment are verified by before/after fingerprints. Credential
worker is online, exactly one Codex2API application is running, and maintenance
route/network gate are removed. Only Codex2API was stopped/started by this task.

Release and recovery details: docs/releases/2026-10-28-intelligence-cooldown.md.
Owned worktree `/Users/gongtengxinwen/.codex/worktrees/intelligence-quota-cooldown/codex2api`,
branch `codex/intelligence-quota-cooldown`. Shared original directory remains
preserved; all 52 recorded file hashes match. Existing snapshots/worktrees are not
deleted. Do not redeploy historical candidates or restore pre-release databases.
