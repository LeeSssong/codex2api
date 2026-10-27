# 2026-10-28 Intelligence cooldown pause/resume (deployed)

## Approved scope

Automatic checks suspend during account cooldown/quota exhaustion, including queued
and claimed-but-not-sent checks. Resume uses current availability, not changes to
saved global/account switches. Frequency, enrollment, manual revisions and history
are preserved. Explicit manual checks and recovery from intelligence-owned quality
pauses retain their existing behavior. No database schema or frontend changes.

## Provenance and integration

Base origin/main: 69abb079e1057952f2f93c7ac5920067a94d9f41.
Live baseline: 9aa624fb088a425668004c45f1d2fa4868a49ee0, executable
7d191c0a7fbec59498ecce290d1d8d7f49c09b0c95cd4c5e55ac5b34a51daf65.
Upstream main: 3c6adfda702efed67931b2d98a491d72bd9d91b5, already contained;
no new upstream commits at preparation, cutover, or post-deployment fetch. Latest
user conflict policy: preserve the currently running production implementation.

## Verification

- RED reproduced actual bugs at enqueue, claim and execution, including an actual
  localhost upstream request while the account was cooling.
- Intelligence suites: SQLite, isolated PostgreSQL behavior matrix, auth/database/admin
  race tests, periodic ticker cooldown/expiry, queued backlog fairness, early reset,
  manual switches, manual promotion, legacy SQLite timezone/subsecond formats.
- Full Go test suite passes after building required embedded frontend assets.
- Frontend: 458 unit tests, typecheck, build, 9 intelligence browser tests pass.
- Release tool suite: 85 checks pass. Independent code review completed; late preparation and WS write-lock findings fixed and re-reviewed with no remaining blockers.
- Initial failed attempts are retained: missing frontend embed assets was fixed by
  the normal build; first test DB port collided with another local Postgres and
  was retried on a dedicated port. No production state was changed by these tests.

## Deployment and recovery

Target only netcup 159.195.12.230. User subsequently explicitly requested direct
downtime deployment, without extra zero-downtime preparation. Never overlap old
and new singleton writers. Retain mandatory backup/rollback safety. Use the tested
maintenance_release.py with /var/lock/codex2api-release.lock, current container/image/
actual executable/Compose/source checks, restored-database rehearsal, stopped
backup, one application instance, preserved credential runtime and API checks.
No Sub2API, PG, Redis, old production or cold-standby restart.

Final production verification: enabled=true, interval=600 seconds, revision=12,
history=103. Settings, account switches and enrollment fingerprints match the
stopped baseline; history was not lost. There are 81 enrollment rows, with 4 account detection switches
enabled; the 81-row count must not be described as 81 enabled accounts. Earlier
history=96 was a stale observation.
Status: deployed and verified; see the exact release/recovery evidence below.

## Final dispatch race review

Independent review identified a cooldown change after the runner eligibility
check but during request preparation. Automatic probes now carry a dedicated
context marker, rechecked before HTTP dispatch and before/after WebSocket pool
acquisition. A typed deferred outcome returns the run to queued without grading
or logging a fake quality failure. Manual probes and ordinary generation do not
carry this marker. Deterministic preparation and real local WebSocket handshake
regressions cover the late transition; the original preparation test failed with
one actual local upstream request before the fix.

The final native WebSocket check also runs inside the connection write lock,
before marking its read lease as sent, so waiting for heartbeat writes cannot
bypass a newly entered cooldown. The real socket write-lock regression failed
before this correction and passes afterward. Full-suite retry evidence retains
an unrelated existing image-job TempDir cleanup race; its isolated five-run
retry passes, without changing that unrelated production logic.

## Released identity and checks

- Source: `e61b04c3b9a72cbd75066ec09f7f75cb`.
- Tree: `23d4b944ea0193fb07bc4b451ee5901954cf4e77`.
- Upstream: `3c6adfda702efed67931b2d98a491d72bd9d91b5`, fetched again and already included.
- Build version: `release-20261028-e61b04c3b9a7`.
- Executing binary SHA256: `5bdc31033b1f3646267de6d1747450918c2c24f67426b2506aef773fc9ec77b2`.
- Actual image/config digest: `sha256:8e4fb65fb86fc0dbf1c6ce60639597c83f36bbe6a8c9dadbcc5a0c6ec03c2dab`.
- OCI build index: `sha256:2be8fd1b060bc2df97ce9687da255e347232fa3df88f1734d21ab08cab2f4512`.
- Container: `404f0d579a0e2e4d73c668eff2ca8e36521de6a5e199c67e7f3a1d387211f545`.
- Compose: `/opt/codex2api/compose.netcup.json`; host port 18087, internal 18080.

Final verification at 2026-10-28 01:22:05 +08:00 checked internal/public JSON APIs,
admin pages, quota stats, version, actual executable, image and Compose identity,
credential encryption settings and fresh worker heartbeat. Preserved business
configuration fingerprints and history floor match; exactly one application is
running; the maintenance route/network gate are removed. No production inference
was manually triggered and no production cooldown or switches were changed for
acceptance. Live health is `ok`; its current capacity fields (`available=0`,
`total=3`) are a separate account-availability observation, not proof of usable
upstream capacity. Cooldown expiry/resumption is verified by controlled tests.

Fresh post-deployment regression tests pass for database/auth/admin/proxy/wsrelay,
including the real periodic runner and preparation/native/relay WS races. Earlier
complete Go, race, isolated PostgreSQL, frontend and release-tool logs remain in
the protected local evidence directory. No schema or frontend production change.

## Actual maintenance and concurrent release incident

All timestamps below are Asia/Shanghai on 2026-10-28:

| Window | Start | Traffic reopened | Duration |
| --- | --- | --- | --- |
| Initial deployment | 01:12:04.386 | 01:12:20.877 | 16.491 seconds |
| Protective recovery | approximately 01:14:19.858 | 01:17:24.560 | approximately 184.702 seconds |

Total maintenance is approximately 201.192 seconds (3 minutes 21 seconds). The
second start uses the recorded rollback-start boundary, immediately before
installing the maintenance gate; it is a conservative approximation rather than
a separately instrumented gate timestamp. Public smoke completed at 01:19:06.

During initial public smoke an external publisher replaced these three Sub2API
containers: `sub2api-openai-reauth-worker`, `sub2api-sub2api-blue-1`, and
`sub2api-sub2api-worker-1`. Protected-container identity verification correctly
rejected the changed baseline; Codex2API was stopped behind maintenance. This
task did not restart or replace those services, PostgreSQL, Redis, old production
or the cold standby.

Recovery held the same `/var/lock/codex2api-release.lock`, verified the exact
candidate and unchanged Compose/source/configuration, and recorded the precise
three external replacements with their old/new identities. It required the new
containers to be healthy, preserved current non-target Caddy routes, retained and
backed up the current database, then restarted the same candidate and restored
only the Codex route. Both internal/public smoke passed against the refreshed
non-target baseline. No protection check was removed; no old dump was restored.
The first recovery attempt could not discover stopped-container runtime ports;
it made no application start or database restore. The corrected attempt obtains
ports from the preserved original inspect record and completed successfully.

## Evidence and recovery entry

Production release directory:
`/opt/codex2api/releases/intelligence-cooldown-20261028-e61b04c3/`.
Backup/evidence directory:
`/opt/codex2api/backups/intelligence-cooldown-20261028-e61b04c3/`.
These contain release.json, final-verification.json, original/failed release
records, preserved configuration and protected-container fingerprints, the
preflight/stopped/current-recovery dumps, Compose and route snapshots. The isolated
restored-database migration rehearsal and stopped backup validation passed.
Protected local evidence is under
`/Users/gongtengxinwen/.config/codex2api/intelligence-cooldown-20261028/`, including
production-evidence, recovery.log, final-verification.log and test logs. Sensitive
raw records remain outside Git.

Exact prior executable recovery image:
`sha256:94d234adf5c04d0e7bc7624acb204451872e79f6d19e7f7d684f35e3e468699f`;
prior binary `7d191c0a7fbec59498ecce290d1d8d7f49c09b0c95cd4c5e55ac5b34a51daf65`.
`compose.rollback.json` is an artifact reference, not a safe blind rollback
command. Production has accepted new writes: never restore stopped.dump or an
older database. Future recovery must hold the release lock, compare the current
source/container/image/executable and preserve latest data, stop the sole writer,
then repair forward or separately establish old-binary compatibility with the
current database. Do not rerun the stopped-only recover-current.py on a running
application or repeat this historical candidate deployment.

All 52 shared-preservation file hashes remained unchanged. This task's code is
merged/pushed; pre-existing uncommitted/shared work remains saved separately,
not included in this release. No conflict resolution discarded running behavior.
Remaining operational issue: a concurrent non-target deployment extended this
maintenance window; its exact baseline transition is recorded. No remaining
code-review blocker or failed final smoke check.
