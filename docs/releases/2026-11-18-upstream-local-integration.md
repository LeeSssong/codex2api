# 2026-11-18 upstream and local integration record

Owner: this chat. Source branch: `codex/upstream-release-20261117`.
Upstream snapshot: `4514623dda4ae025fde5af439345c1c0ba641a7c`.
Authoritative production remote: `LeeSssong/codex2api`, branch `main`.

The user authorized merging all local uncommitted changes, pushing main, and a downtime deployment of Codex2API on netcup `159.195.12.230`.

All 52 original modified/untracked files were preserved before reconciliation in commit `289ea37c70e8c97e5a7349d2b3113658e1a1a34a` on `codex/preserved-shared-20261117`. Exact protected copies and hashes are under the local task evidence directory. The original shared files were rechecked against those hashes before integration.

The release source is the clean main commit containing this document. Its source tree and binary/image digests are recorded by the verified build manifest and the production source record. Earlier task-context deployment statuses are historical.

## Local change disposition

Preserved and adapted: quota estimate/account response UI, concurrency retention on degradation, session identity administration, client persona slots and persistent conversation recall, fingerprint/environment/telemetry updates, turn-state handling, Windows attestation helper, and local Grok/identity regression coverage.

Updated upstream equivalents supersede stale implementations: current model catalogs and routing, checked client-version resolver, current attestation dispatch, store/outbox APIs, settings payload/type structure and refreshed locale/documentation content. Local-only UI keys and CSS are retained.

Additional integration fixes preserve recovery scheduling and lease release, isolate converged WebSocket transport by API key, capture response ownership per request, recall continuations before incremental-content derivation, and keep configured turn-state precedence consistent. Model quality scheduling gates remain active for internally scheduled requests; externally pinned requests retain upstream capability-only behavior.

The following original paths were individually included in the reconciliation inventory; raw originals remain recoverable from the preservation commit:

- `admin/account_page_stats.go`
- `admin/account_page_stats_test.go`
- `admin/account_response_builder.go`
- `admin/codex_session_identity.go`
- `admin/codex_session_identity_test.go`
- `admin/handler.go`
- `admin/handler_test.go`
- `admin/test_connection_usage.go`
- `admin/usage_probe.go`
- `auth/scheduler_outbox_consumer.go`
- `auth/store.go`
- `auth/store_scheduler_test.go`
- `docs/collaboration.md`
- `frontend/src/api.ts`
- `frontend/src/components/AccountQuickConfigSheet.tsx`
- `frontend/src/lib/accountQuickConfig.test.mjs`
- `frontend/src/lib/accountQuickConfig.ts`
- `frontend/src/lib/settingsPayload.test.mjs`
- `frontend/src/lib/settingsPayload.ts`
- `frontend/src/locales/en.json`
- `frontend/src/locales/zh.json`
- `frontend/src/pages/Accounts.tsx`
- `frontend/src/pages/ClaudeAccounts.tsx`
- `frontend/src/pages/Settings.tsx`
- `frontend/src/pages/docs/docsContent.ts`
- `frontend/src/types.ts`
- `proxy/codex_conversation_identity.go`
- `proxy/codex_environment_context.go`
- `proxy/codex_fingerprint.go`
- `proxy/codex_fingerprint_modes.go`
- `proxy/codex_fingerprint_modes_test.go`
- `proxy/codex_models_manifest.go`
- `proxy/codex_persona_slots.go`
- `proxy/codex_telemetry.go`
- `proxy/codex_telemetry_events.go`
- `proxy/codex_telemetry_test.go`
- `proxy/codex_turn_state.go`
- `proxy/codex_turn_state_inject.go`
- `proxy/codex_ua_catalog.go`
- `proxy/codex_ua_catalog_test.go`
- `proxy/executor.go`
- `proxy/grok_default_models_test.go`
- `proxy/grok_models_registration_test.go`
- `proxy/subscription_sync.go`
- `proxy/usage_wham.go`
- `proxy/usage_wham_breakdown.go`
- `proxy/usage_wham_daily.go`
- `proxy/user_agent_audit.go`
- `proxy/windows_desktop_attestation.go`
- `proxy/windows_desktop_attestation_test.go`
- `proxy/wsrelay/executor.go`
- `proxy/wsrelay/fingerprint_identity_test.go`

## Release procedure and authoritative receipt

Build only after source/remote verification. The prepared controller must hold `/var/lock/codex2api-release.lock`, compare the live container/image/binary/Compose/source baseline twice, preserve an exact binary rollback image, restore a disposable PostgreSQL copy and rehearse migrations, then enter maintenance, drain and stop the sole Codex2API writer. Take and verify a stopped backup before migration and startup. Preserve mounts, credential worker, account switches, all intelligence configuration and history. Restore traffic only after internal smoke passes; verify public health and persist provenance.

Production artifacts and rollback receipt use `/opt/codex2api/releases/upstream-local-20261118-<source-prefix>/` and `/opt/codex2api/backups/upstream-local-20261118-<source-prefix>/`. The exact release ID is in `/opt/codex2api/source-release.json`. Read `final-verification.json`, `release.json`, `retained.before.json` and the stopped dump in that backup directory for actual outcome and timings. Local evidence is `/Users/gongtengxinwen/.config/codex2api/upstream-release-20261117/`. These runtime receipts carry values that only exist after this source commit is built.

All seven existing inference accounts are administratively disabled. Preserve that state; health/API/UI/worker checks do not require enabling accounts or sending real inference requests. Do not restore a stopped database dump over subsequent writes. Revalidate the current schema, live source identity, latest data and rollback image before recovery.

## Verification evidence

Frontend unit suite: 534 passed; typecheck and production build passed. Intelligence browser suite: 13 passed. Quality SVG suite: 5 passed with one optional local-sample skip. Version-check browser suite: 9 passed. Release controller/tool tests: 102 passed. Focused race verification of conversation ownership/isolation, persona repair allocation and intelligence scheduling passed across proxy, wsrelay, admin and database packages. All Go packages passed across the full package run and the isolated full proxy rerun. The monolithic run had only a statistical proxy performance failure while browser checks were using CPU; the full proxy rerun passed with its original performance gates. `go vet -p 1 ./...` passed. API-key portal, state-management, Codex routes, IPv6 egress and model-quality browser scripts passed on their desktop/mobile and theme scenarios. The IPv6 fixture includes the upstream fixed-egress sibling API and current localized labels. Exact logs are retained in the protected task evidence directory. A disposable copy of the actual production PostgreSQL database is additionally required to pass migration and retained-configuration verification before the maintenance window.
