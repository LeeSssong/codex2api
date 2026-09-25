# Codex2API Native Smart Operations Implementation Plan

> For agentic workers: Use subagent-driven-development in separate worktrees. Implementation AND production deployment are approved.

Goal: Deploy complete quality guard, account alerts and OAuth credential guard on verified hloolx production baseline.
Architecture: Reuse existing accountops for quality/alerts; add persistent TokenGuard with credential-generation/control-revision CAS. React reuses native accounts/auth/theme. Preserve hloolx BPS/State/routing.
Tech stack: Go1.26.6, Gin, React19, TypeScript/Vite, SQLite/PostgreSQL, Redis.
Spec: docs/superpowers/specs/2026-09-25-codex2api-smart-ops.md

## Global Constraints
- One writer per worktree; root alone integrates and deploys. Agents never access production.
- Root target is sub2api-prod / 64.83.10.67 / codex.xingqiaolab.top. Test and legacy hosts are out of scope.
- Preserve original uncommitted locale/Grok patches and existing production hloolx behavior.
- No secrets in output/logs/repo. No real model or notification calls in automated tests.
- Explicit incorrect quality results alone may isolate; transient failures do not.
- Recovery preserves manual disable, other guards and cooldowns; control revision is distinct from credential generation.
- Native RT lease and consumption journal remain authoritative. Relogin publication needs identity, generation, state revision and job fence CAS.
- Sub rule imports require unambiguous identity/group/model mapping and are paused; never assume matching numeric IDs.
- Database changes deploy via single-instance maintenance, verified restore and retained old artifacts.

### Task 1: Verified baseline (root)
- [x] Create isolated branch from origin/main and merge history with running hloolx b402c611; application tree follows production baseline; preserve owned release scripts/docs. Commit7477f7cb.
- [ ] Preserve original patch and file hashes before main integration; restore after release.

### Task 2: Quality and account-alert backend
Files: accountops/*; database/account_ops.go,account_quality.go and tests; admin/account_ops.go/tests; proxy/account_ops_observer.go/tests. Selective wiring in executor files, admin/handler.go, admin/quality_test_handler.go, database/quality_tests.go, database/postgres.go, main.go, go.mod/go.sum.
Interfaces: retain old main4adc5847 /account-ops/module,/config,/alerts and /quality-ops/plans,/history JSON APIs; existing Start/StopAccountOps. Guard agent supplies control_revision foundation and transaction helper; coordinate before final recovery protection tests.
- [ ] Restore existing module/tests from4adc5847 and patch hooks into latest hloolx without overwriting whole shared files.
- [ ] Add regressions for manual account/group change preventing recovery, cross-guard ownership, inconclusive non-action, all-round-correct recovery and actual BPS-path attribution/observer coverage.
- [ ] Run go test ./accountops ./database ./admin ./proxy -run 'AccountOps|AccountQuality|QualityOutcome|Judgment|Plan|SMTP' -count=1 plus affected native QualityTest/BPS checks.
- [ ] Commit and report RED/GREEN evidence for new behavior, exact tests and gaps. Do not touch frontend.

### Task 3: Credential guard backend
Files: tokenguard/*; database/token_guard*.go; auth/token_guard*.go; admin/token_guard*.go; minimal state revision hooks in database/postgres.go and auth/store.go. Root wires Handler/main/routes; do not collide with quality worker.
Interfaces: shared HTTP contract below. Export Handler.StartTokenGuard(ctx context.Context), StopTokenGuard and handler methods. Standalone ensureTokenGuardSchema helper for DB initialization. First provide accounts.control_revision migration/trigger and reusable transaction accessor to quality worker.
- [ ] Implement and test SQLite/PG monotonic control revision covering account control and group changes; send foundation commit immediately.
- [ ] Port complete Sub config/probe/NDJSON/secret-mask behavior with explicit account_id login mapping and eligible OAuth scope.
- [ ] Implement durable jobs, owner lease/fence, cancel/restart, bounded workers, states/events/ownership and config-change fencing.
- [ ] Implement verified-failure isolation and long external relogin, then short native credential-lock/CAS publication; never bypass RT journal or reuse stale capability evidence.
- [ ] Preserve manual changes, other isolation and cooldowns during repair/recovery; use native outbox/cache synchronization.
- [ ] Add authenticated handlers and lifecycle/module gating; mock tests cover races, stale ownership, transient errors, masks, scope, secret redaction and both DB drivers.
- [ ] Commit and report required init/routes/lifecycle hooks and evidence.

### Task 4: React native integration
Files: frontend/src/pages/AccountQuality.tsx,AccountOps.tsx,TokenGuard.tsx,account-ops.css; components/SmartOpsNav.tsx; lib/accountOps.ts,tokenGuard.ts/tests; api.ts; App.tsx; Layout.tsx; locales; tests/smartOps.browser.test.mjs.
Interfaces: retain existing quality/alert wire contracts; use TokenGuard contract below. Backend agents never edit frontend.
- [ ] Restore full quality/alerts pages and use approved three-tab native smart-ops structure with current components/theme/i18n.
- [ ] Implement every TokenGuard source setting, account-ID/email/password/MFA mapping, masked-secret preservation, status/events, async manual run/relogin/cancel and clear conflict/busy/error/saved states.
- [ ] Distinguish credential repaired from scheduling restored. No sample/fake data in production.
- [ ] npm ci; npm run typecheck; related Node tests; npm run build; mock browser smoke and desktop/mobile screenshots. Each worktree owns node_modules/dist.
- [ ] Commit and report evidence.

### Task 5: Provenance and release controller (root)
Files: admin/system_update.go/tests and source-provenance helper; internal/version; frontend hooks/version UI in coordination; deploy/release/release_host.py/tests; native mapping/import helper.
- [ ] Managed build checks hloolx commits and exposes upstream+integration revisions; refuses container binary self-update and legacy james release replacement.
- [ ] Adapt controller from standard baseline (old account-ops404), preserve settings, smoke all3 modules, assert unrelated container IDs unchanged and verify exact digest.
- [ ] Read-only account/group matching; import Sub quality plan paused only when unique and preserve source linkage/history.
- [ ] Test updater/controller rollback and migration behavior without production side effects.

### Task 6: Integration and production (root)
- [ ] Cherry-pick agents, resolve actual integration conflicts, run affected package/DB/frontend checks and one focused whole-change review.
- [ ] Fix confirmed findings and rerun affected checks only.
- [ ] Preserve local edits, advance/push root main; verify clean non-detached main commit/tree == origin/main. Build once and pin digest.
- [ ] Announce downtime/schema scope; validated backup, stop writing/drain up to300s, migrate one instance, start/health/version/admin feature checks, restore traffic. Automatic rollback on failures preserving resumed writes.
- [ ] Restore existing local patch, verify Sub unchanged, write one release record. Test station stays explicitly unqueried.

## Shared TokenGuard HTTP Contract
Base: /api/admin/account-ops/token-guard; existing admin authentication required.
GET /status returns {config,accounts,events,runtime,module_enabled}. Config/state/event fields follow Sub frontend/src/api/admin/accountTokenGuard.ts. Runtime additionally allows job_id,job_state,cancellation fields.
GET /config and PUT /config exchange full config. Secrets masked ********; omitted/blank/mask preserves existing values. relogin_accounts[] adds account_id:number with email,password,mfa_secret. Invalid config400 sanitized.
POST /run and POST /accounts/:id/relogin return202 {job_id:string,state:string}. Existing incompatible job409. Manual actions use durable jobs.
POST /jobs/:id/cancel returns {cancelled:boolean}; fence prevents late commits.
GET /events?before_id=<id>&limit=100 returns {items:TokenGuardEvent[],next_cursor:number}.
Source config fields: enabled,group_ids,interval_seconds,probe_endpoint,probe_model,probe_headers,probe_timeout_seconds,probe_concurrency,max_probe_per_cycle,auto_relogin,relogin_endpoint,relogin_headers,relogin_accounts,restore_schedulable,fail_streak_threshold,bark_key,notify_on_fix,notify_on_fail.
