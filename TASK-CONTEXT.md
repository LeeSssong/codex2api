# Independent smart operations upgrade — active task

User explicitly approved implementation and final production deployment. Hard constraint: never alter or interrupt Sub2API. Codex2API and Sub2API must be completely independent applications; no shared business services/plugins/data/credentials/workers. Source code ports are permitted, independently versioned.

## Working state
- Integration: `/Users/gongtengxinwen/Documents/codex2api/.worktrees/independent-plugins`, branch `codex/independent-plugins`.
- Root repository `/Users/gongtengxinwen/Documents/codex2api` remains old main e557d171 with THREE ORIGINAL dirty paths frontend/src/locales/zh.json, proxy/grok_default_models_test.go, proxy/grok_models_registration_test.go. Preserve. Latest origin/main and production are 93d33210; fetched remotes only. Production not mutated yet.
- Latest official upstream 7d717d7f merged in a7517e92 (actual production baseline already had 56015088). No conflict leftovers.
- Root integrated partial policies scaffold 1ce0bf/10566c; REAL policies agent is replacing it. Do not mistake scaffolds for working features.
- Restored old core backend/UI commits through e0afabf3; real core reviewer is repairing gates and adding missing BPS actions. All root changes are committed.
- User-approved plan: docs/superpowers/plans/2026-10-03-independent-smart-ops.md.

## Current active agents and ownership
- `/root/core_review_fix`: plugins-core worktree; full review/fix restored quality/alerts/tokenguard/plugin gates, BPS enable/recovery, actual observers and UI navigation. gpt-6.1-sol high. Previous core agent finished; do not redispatch its stale reports.
- `/root/policies_real_integration`: plugins-policies worktree; durable auto config (including model billing/BPS/default/concurrency hooks), actual scheduler scoring, real durable leased Pelican plans/executor/billing/history/UI. gpt-6.1-sol high. Expected Handler.smartOps and InitSmartOps/RegisterSmartOps/WaitSmartOps hooks. Gives exact contracts later.
- `/root/credentials_real_integration`: plugins-credentials worktree; replaces broken 2FA scaffold with encrypted pending import AFTER real login, durable jobs/config/leases/cancel/fences/native publication, V2 monitors, worker + engine support and UI. gpt-6.1-sol high. API worker outside admin auth at /api/internal/credential-ops; register worker engine routes + scheduler hooks later.
- Root owns integration, Plugins.tsx, release scripts, testing, final review/integration/main/push/build/deploy. One writer per worktree. Never deploy incomplete modules.

## Validation and environment
- Root release tests 44 passed after adding dedicated credential-runtime compose boundary. Further integration tests needed for runtime lifecycle changes.
- Official baseline proxy/prompt-filter/config/database SQLite passed; frontend 380 tests/typecheck/build passed before port. Restored core Go packages passed but reviews found real missing behavior; fixes pending. Frontend restored APIs corrected in 49ac9889; typecheck/build pass.
- Broad baseline PostgreSQL database suite had cross-test pollution logs=3 in TestImageUserBillingPersistencePostgres; isolated fresh DB exact test passed. Do not claim full PG suite passed.
- Local disposable PostgreSQL docker `codex-independent-plugins-test-pg` uses tmpfs; port 127.0.0.1:15439, trust synthetic test only, databases core_test, credentials_test, policies_test, codex_plugins_test, image_billing_isolated. No existing containers deleted. Docker VM disk full; no pruning authorized/needed. `postgres://postgres@127.0.0.1:15439/<name>?sslmode=disable`.
- Local app process exec session 93743 runs older candidate at port18129, SQLite `/var/folders/26/3qc7y_lx2s11df_9sh7dqg_40000gn/T/codex-independent-ui-mj6_fif0/app.db`, Memory cache, synthetic ADMIN_SECRET `codex-independent-local-test-only`, localhost bind. Restart/rebuild for new code later. Initial app warns /data/images unavailable; configure local image dir if testing images.
- Browser skill read, persistent node REPL bindings `agent`, `browser` (iab), `localTab` visiting local plugins route. Only supported browser runtime via tools.mcp__node_repl__js. Local login/bootstrap done. Plugin switch off/on saved and confirmed via actual API. Screenshot `/var/folders/26/3qc7y_lx2s11df_9sh7dqg_40000gn/T/codex-independent-ui-mj6_fif0/plugins-desktop.png`. Layout label plugins.title raw found, reviewer fixing.
- toSub2 source separately fetched pinned HEAD 8548397e89bf80e508eda64a87e0d556d43abc84 at `/var/folders/26/3qc7y_lx2s11df_9sh7dqg_40000gn/T/codex2api-login-engine-8s8q5992`.
- Turb source HEAD d32e49e623dddf71b5fa6f0f5b0250bef963bdbd at `/var/folders/26/3qc7y_lx2s11df_9sh7dqg_40000gn/T/codex2api-email-engine-9adt_t2q`.

## Production release
- SSH alias sub2api-prod=root@64.83.10.67. Only codex2api target permitted. Running codex image codex2api:release-93d33210ebfb, container started 2026-10-01T21:05:36Z. app /opt/codex2api/docker-compose.yml is JSON, mode600. Port18080, own codex2api-net, own codex2api-postgres/redis. Source release script scoped --no-deps.
- Sub protected baseline: API green dfdeb4a5360e9b68bfc18dc6e45ad70cb09cd0c61019d1b2d28c73a8dced38c1 started 2026-10-02T19:15:21.352733449Z; worker575b897b... started17:50:06Z, reauth0723c2cb... started17:50:12Z. All healthy read-only checks. Refresh actual baseline at deployment.
- Source constraints read fully in Sub docs/project/acceptance-station-global-constraints.md. User authorized code merge/push/deploy; new DB changes expected, codex-only maintenance with backup restore rehearsal, up to300s drain, Sub untouched.
- Root adapted release_build.py origin gate (prior wrong production remote), optional --docker-host ssh://sub2api-prod --builder <dedicated>; compile Go/frontend LOCAL, package image remote dedicated capped builder because Docker local disk full. Still need create dedicated builder (NOT Sub builder), build once after clean pushed root main. Script saves local image.tar + manifest; labels require commit/tree/upstream. Source must clean non-detached main == verified origin/main. Preserve root dirty changes before main integration, restore after safely.
- release_host.py protections dynamic all unrelated container ID+StartedAt, rejects any non-Codex Caddy config change. Existing schema deployment backs up/restores own codex DB only and stops own app only. Runtime integration 840a6956 adds args --credential-runtime-image/digest/app-env/worker-env, own worker start/health/rollback. Needs review and smoke endpoint update when actual contracts complete.
- credential_runtime_release.py pure config helper adds service `credential-runtime`, container `codex2api-credential-runtime`, no public ports, own network, cpus0.5/mem512m/read_only/tmpfs64m, --healthcheck via worker. App env private /opt/codex2api/secrets/credential-app.env KEY+WORKER_TOKEN; worker dedicated /opt/codex2api/secrets/credential-worker.env TOKEN only. DO NOT copy Sub env. Keys still not generated, no production mutation yet. Optional Session Studio endpoint/headers would require protected app config; no default shared remote destination.
- No testing against standalone test station or legacy server; not queried/not synchronized.

## Next
Wait for real agent work while doing scoped release review/prep, integrate exact commits (avoid duplicate registry/UI cherry picks), fix conflicts preserving ours. Run actual app + PostgreSQL new feature tests, browser desktop/mobile, scoped independent review. Ensure real task execution not stubs and all 7 modules +2FA source behaviors. Finish required build/deploy with Sub protection and one concise release record. Do not stop at partial helpful implementation.
