# Credential relogin state fix (local only)

User approved implementation on 2026-10-28 and explicitly excluded deployment.
Owner/worktree: current chat, `codex/credential-relogin-state-audit`, base `69abb079` plus diagnosis `6aed5588`.

## Design

The successful native Codex login commit must reconcile SQL, the shared unauthorized cooldown and runtime publication before returning success. Replayed completion and successful-task retries must retry the same reconciliation without consuming another login or clearing newer real failures. Excel grants do not authorize clearing the main Codex status. Quota cooldowns, manual switches and history stay intact.

Use a Store credential-recovery write gate around the short commit/reconciliation section; cooldown publication takes its read side. Retire replaced runtime objects and suppress their later scheduler/cooldown publication. Cache removal checks the reason and task completion timestamp and atomically compares the bytes, so changed/newer entries survive. Cache read/delete or reload errors return a recoverable failure while the durable task remains succeeded. No schema migration or frontend changes.

## Implementation and verification

1. Keep the existing end-to-end red regression; extend coverage for late old-object 401s, completion replays/retries, cache outages, Excel grants, newer auth failures, unrelated quota cooldowns and manual switches.
2. Implement a narrowly scoped Store recovery/publication gate and conditional cache cleanup. Route successful completion and replay/retry through a shared admin recovery helper; preserve existing task/credential generation fencing.
3. Run targeted red/green tests, repeated/race tests on the affected paths, package checks and build. Review the diff for new state clearing and callback error handling.
4. Update investigation/handoff with results and a local commit. No push to main, production account/data changes or deployment. See the operational caveat below for the cleaned-up remote test-container mistake.

## Review focus

- Old in-flight publication ordered before commit or discarded after replacement.
- No unconditional Redis deletion and no broad cooldown reset.
- Replays cannot erase a newer credential-generation failure or unnecessarily reload an already current runtime account.
- Successfully saved task is not turned into a failed/requeued login when runtime reconciliation is pending.
- Preserve independent Excel grants and manual pause/monitor/auto-login controls.


## Implemented candidate

- Local commits before final review: diagnosis `6aed5588`, implementation `55dfa24a`, upstream integration `611c69fd`. Integrated upstream main: `20594822cde18233e812fd5ce3d279c598e75416`; original base: `69abb079e1057952f2f93c7ac5920067a94d9f41`. Nothing pushed or deployed.
- Native Codex completion/replay/succeeded retry share generation-checked reconciliation. Successful-task descendants use the returned task ID. Initial-import dedup persists the resolved expected generation. Excel completion never clears the independent Codex failure.
- Compare-and-delete reads only the account runtime-value namespace (not owner keys). Generation-stamped cache merges favor newer credentials. Legacy generation-zero auth entries use the completion timestamp with SQL second-precision tolerance; same/new-generation entries never use that fallback.
- SQL runtime-state writes require the account credential generation. The Store write gate encloses commit/reconciliation only, not upstream login. Normal cooldown and transient throttling publication take its read side. Retired runtime objects cannot republish; scheduler checks retirement while holding its own lock.
- Cache/reload failure remains a retryable 503 after durable success, not a second login or task rollback. Monitor enrollment, enabled/auto flags, intervals/rules, manual pause, quota cooldowns and history are preserved.
- Final review reproduced an old-publisher scheduler race with a deterministic red test, then fixed it inside `FastScheduler.updateLocked`. Reverted global generation normalization after full-suite proxy fallback regressions; normalization stays specific to cooldown persistence/hydration.

## Verification scope and operational caveat

Tests use synthetic SQLite accounts/tokens, memory-cache failure injection and stubbed probes; no live credentials or real upstream login. Frontend dependencies were installed with scripts disabled and the normal frontend build generated the ignored `frontend/dist` needed by Go embedding. Frontend build passed with only its large-chunk warning; no frontend source changes.

A real Redis integration attempt did not pass: Docker was unexpectedly pointed at the existing remote `sub2api-prod-build` context (`ssh://sub2api-prod`). The isolated `codex2api-relogin-test-redis` container had no volumes or application code and was removed immediately; exact-name listing confirmed cleanup. The user was informed. No application deployment, production account/data modification or production service restart was performed. No further Docker/SSH/remote-container operations will be used for this task. Default-suite optional Redis/PostgreSQL integration tests are skipped without their services, not counted as integration successes.

## Final reviewed-source verification — passed locally

The sequential race/full-suite/build chain completed with exit code 0 on the reviewed code, including the final scheduler retirement guard and proxy-compatible generation accessor.

```sh
GOMAXPROCS=4 go test -race -p 2 ./admin ./auth ./cache ./database \
  -run 'TestCredentialOpsRelogin|TestCredentialOpsExcelGrant|TestCredentialRuntimeStateRejects|TestCredentialOperationsInitialImport|TestMemoryRuntimeCooldownMerge|TestUsageCooldownDelayedPublisherRespectsCurrentState|TestUsageCooldownRetriesFailedPersistence|TestTransient|TestFastSchedulerRetiredAccountCannotOverwriteReplacement' \
  -count=3 -timeout=180s
GOMAXPROCS=4 go test -p 4 ./... -count=1 -timeout=300s
GOMAXPROCS=4 go build -p 2 ./...
git diff --check
```

- Race repetitions: admin 24.822s, auth 15.723s, cache 1.778s, database 10.596s; no race reports.
- Full suite: 24 packages passed; packages without tests reported normally. Optional real-service integrations were not exercised.
- Build and whitespace validation passed. Logs: `/tmp/codex2api-relogin-reviewed-race.log`, `/tmp/codex2api-relogin-reviewed-tests.log`, `/tmp/codex2api-relogin-reviewed-build.log`.
- Deterministic scheduler regression previously reproduced the failure (`late retired update removed the fresh account`), then passed three repetitions after the under-lock guard. Historical red/green logs: `/tmp/relogin-scheduler-race-red.log`, `/tmp/relogin-scheduler-race-green.log`.
- `git ls-remote origin refs/heads/main` still returned `20594822cde18233e812fd5ce3d279c598e75416` at final review. Upstream intelligence-cooldown work is merged and preserved, not redeployed.

Candidate complete locally; not pushed, not merged into main, not deployed. The final commit after `611c69fd` records the reviewed code and this handoff; resolve it with `git log -1` rather than a self-referential commit hash. The user must authorize any later deployment separately.
