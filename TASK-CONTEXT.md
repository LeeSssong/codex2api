# Current status: relogin + intelligence recovery interval deployed

User authorized downtime deployment on2026-10-28. Both tasks merged/pushed to main;
production source `884392a122b3abcaab74b1cd20cd05111aab06a3`, tree `6f8aafa7f9fab4a1664de856502d2882f8e60a0d`,
actual binary `bba286e114ece31d2a4f5d4517018c06572456ca1e31508119f3996cd8a7e4a9`,
image `sha256:38e6e7997290eeb5642fe712751a54091e685a7e4a1ae800ee04785f764bfcd5`. Later documentation commits do not require redeploy.
Maintenance 2026-10-28T02:22:31.216+08:00 to 2026-10-28T02:22:44.124+08:00
(12.907 seconds); independent final verification passed. Exactly one writer,
worker online, maintenance removed, unrelated containers unchanged.
Current settings preserved: enabled=false, ordinary interval600, revision13;
new recovery interval3600 returned without changing saved intent. History [104, 104].
No live login probe, Sub2API/database/Redis restart or cold-standby action.

Evidence/recovery: docs/releases/2026-10-28-relogin-recheck.md. Current production
has new writes; never restore older snapshots or redeploy stale candidates.
Interval source5aaf8ad5 and credential355d46e4 merged via e0480e24; all10 interval
snapshot hashes match; shared dirty original remains preserved. Release clone
main is independent of shared original; no forced main update.

Earlier handoffs follow as history; pending/local-only statuses are superseded.

---

# Active release: relogin state + intelligence recovery interval

2026-10-28 user explicitly authorized merging `codex/intelligence-recheck-interval`
and downtime deployment to netcup159.195.12.230. This supersedes the earlier
local-only instruction for this release.

Interval worktree's10 dirty files were snapshotted with hashes outside Git,
committed `5aaf8ad5`, merged via `e0480e24` with credential head `355d46e4`.
Upstream `20594822` is included. Shared dirty original remains untouched.
Merged full Go/race tests,458 frontend units, typecheck/build,9 browser tests,
85 release checks and isolated LOCAL Redis/PostgreSQL integration passed.

Production baseline e61b04c3, live detection revision13/enabled=false/interval600;
preserve current switches/settings/history. New recovery interval defaults3600
at read time. Build clean pushed main in an independent clone. Deploy only via
common lock and maintenance_release.py with exact identity checks, restored DB
rehearsal, stopped backup and single writer. Result:
`docs/releases/2026-10-28-relogin-recheck.md`.

Earlier handoffs below are historical and their no-deploy limit is superseded.

---

# Current task: 2026-10-28 credential relogin state — local only

Latest user instruction: implement the diagnosed fix, **do not deploy yet**.
Owner/worktree: current Codex chat,
`/Users/gongtengxinwen/.codex/worktrees/credential-relogin-state-audit/codex2api`;
branch `codex/credential-relogin-state-audit`. Original base `69abb079`;
diagnosis `6aed5588`, implementation `55dfa24a`, merged upstream `20594822`
via `611c69fd`. Final reviewed candidate is the branch head after `611c69fd`; nothing pushed,
merged into main or deployed by this task.

The fix reconciles committed Codex credential generation, shared unauthorized
cooldown and runtime state on completion/replay/retry. Conditional SQL writes,
generation-aware atomic cache cleanup and retired-object scheduler guards keep
late old requests from re-banning the replacement. Quota/manual pause/monitor
settings and independent Excel grant state remain intact. No frontend source
changes or schema migration. Local implementation is complete: full Go suite (24 packages), three race
repetitions, Go/frontend builds and diff validation passed. Evidence is in
`docs/investigations/2026-10-28-credential-relogin-fix-plan.md`.

Operational caveat: optional Redis integration accidentally used the existing
remote Docker context `sub2api-prod-build`. Only isolated temporary no-volume
`codex2api-relogin-test-redis` was started, then immediately removed; exact-name
listing confirmed absence and user was informed. No application deployment,
production account/data change, real login or production service restart.
Real Redis/PostgreSQL integration is not verified. No further remote operations.

The following production handoff is **preserved upstream history**, not a
release or live-state verification performed by this credential-relogin task.
Do not redeploy historical candidates or infer deployment authorization.

---

# Preserved upstream handoff: 2026-10-28 intelligence cooldown fix — deployed

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
