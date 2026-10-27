# 2026-10-28 Intelligence cooldown pause/resume (release candidate)

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
no new upstream commits at preparation. Fetch again before publishing. Latest
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

Current production detection setting observed at preparation: enabled=true,
interval=600 seconds, revision=12, history=96; these are observations, not values
to overwrite. Re-read and preserve current values at deployment.

Status: not deployed. Exact artifact IDs, maintenance duration and recovery paths
will be appended after verified deployment; do not infer deployment from this file.

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
