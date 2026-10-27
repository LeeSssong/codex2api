# Credential Operations relogin leaves stale unauthorized state

## Historical diagnosis scope and baseline

This section records the investigation before implementation was authorized. Its line numbers and red-test results refer to that original baseline; see the implementation follow-up below for the current local candidate.

User requested investigation only. No production account changes, reauthentication, service restart or deployment performed. The production manifest read in this chat identified commit `9aa624fb088a425668004c45f1d2fa4868a49ee0`; relevant completion, database commit, reload and cache code is unchanged at investigation base `69abb079e1057952f2f93c7ac5920067a94d9f41`.

Owner: current Codex chat. Branch: `codex/credential-relogin-state-audit`. Worktree: `/Users/gongtengxinwen/.codex/worktrees/credential-relogin-state-audit/codex2api`. Only a regression reproduction test and documentation were added; application behavior remains unchanged.

## Confirmed root cause

1. `database/credential_operations_lifecycle.go:438` commits replacement Codex credentials and clears the SQL authorization error/cooldown.
2. `admin/credential_operations.go:437-451` calls `reloadTokenAccount` after commit, but never invalidates the old shared account cooldown cache.
3. `admin/oauth.go:543-554` removes/reloads the runtime account from SQL. Its fresh state initially reports `active` and empty error text.
4. A normal scheduler selection reads the old `account-cooldown` cache entry in `auth/store.go:4427-4444` and applies it. `applyCachedAccountCooldown` restores the unauthorized cooldown and banned health tier without restoring an error detail. `RuntimeStatus` therefore reports `unauthorized`, `ErrorMsg` remains empty, and the account is not dispatchable.
5. Successful manual connection tests call `RecordManualTestSuccess` (`auth/store.go:11409-11448`), which clears the runtime failure state and persists/deletes the cooldown cache. This explains the user's observed recovery without another relogin.

The native OAuth recovery path already documents and performs shared-cache invalidation in `clearReimportedAccountErrorState` (`admin/oauth.go:468-488`). Credential Operations does not use an equivalent step. This is a backend consistency bug, not only stale frontend state.

## Offline verification

All identities, JWT-shaped credentials, encryption material and cache/database contents in the added test are synthetic. Usage and capability network probes are stubbed. SQLite and the in-memory implementation of the same runtime cache interface reproduce the missing invalidation; no real upstream requests are required.

Baseline tests passed:

```sh
go test ./auth ./admin -run 'Test(ForgetCachedAccountCooldownDropsRecord|CredentialOperationsCodexCompletionReplacesExcelClientMetadata|RecordManualTestSuccessRecoversReadyBannedAccount)$' -count=1
```

Added regression intentionally fails on the unmodified application, including three consecutive repetitions:

```sh
go test ./admin -run '^TestCredentialOpsReloginMustClearCachedUnauthorized$' -count=3 -v
```

Each repetition records:

```text
completion: HTTP=200 task=succeeded SQL=active runtime=active error=""
BUG: successful relogin left the previous unauthorized cooldown in cache
BUG: fresh valid credentials are still rejected by the scheduler
after scheduling: runtime=unauthorized error="" available=false
BUG: scheduler restored unauthorized state with no error detail after successful relogin
after manual test success: runtime=active error="" available=true
```

`git diff --check` passed. The regression is deliberately red pending a real fix; this report does not claim the bug is fixed or that the complete test suite passes.

## Original recommended fix boundaries (before implementation)

- Successful main Codex credential replacement must invalidate the stale authorization cooldown in coordination with runtime reload, including idempotent completion/retry recovery paths.
- Do not clear Codex authentication failures merely because a separate Excel grant completed.
- Preserve unrelated quota/rate-limit cooldowns, manual pause/enable flags, monitoring settings, credential-generation guards and historical records. Avoid global Redis flushes, broad cooldown resets or unconditional test-success calls.
- Verify cache invalidation failure and runtime reload failure handling; account for in-flight old-generation responses without erasing a newer legitimate failure.
- Cover initial completion, replay/retry after partial runtime recovery, independent Excel grants and genuine quota cooldowns before requesting production deployment.


## Implementation follow-up — 2026-10-28, local only

User subsequently authorized implementation and explicitly excluded deployment. The candidate reconciles the committed credential generation, shared unauthorized cooldown and runtime account on initial completion, completion replay and successful-task retry. The original red regression now passes. Only a stale unauthorized entry is removed using byte compare-and-delete; same/new-generation failures, concurrent cache replacement, unrelated cooldowns and independent Excel grants are preserved.

Cooldown persistence is fenced by the SQL credential generation and a short Store recovery/publication gate. Removed runtime objects are retired, including a second check under the scheduler lock, so late old-generation responses cannot remove or re-ban a replacement. Cache/reload failures return a recoverable HTTP 503 while the durable login task remains succeeded; recovery does not require a second login. Raw credential-generation access retains existing proxy fallback semantics; legacy generation normalization is limited to cooldown handling.

Implementation and verification details: `docs/investigations/2026-10-28-credential-relogin-fix-plan.md`. The earlier diagnosis and production manifest in this document are historical evidence, not the current deployed baseline. This task has not deployed the fix or pushed any branch.

Operational caveat: an optional Redis integration attempt inadvertently used the existing remote Docker context (`sub2api-prod-build`) instead of a local daemon. Only the isolated temporary `codex2api-relogin-test-redis` container was started, with no volumes and no application deployment; it was immediately removed and exact-name listing confirmed absence. This was disclosed to the user. No production account changes, real login or production service restarts were performed. Do not interpret this task as having made no remote operations. Real Redis and PostgreSQL integration remain unverified; no further Docker/SSH/remote-container operations are authorized by this local-only implementation.
