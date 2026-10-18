# Production + official + hloolx integration (2026-10-16)

## Fixed sources and scope

- Production baseline: 90f8dd0f72ad89ade96931944e621feeeed26d88.
- Official source: 8cd964bb03ac424e16de45f5405bd89b6a45e8d6.
- First merge commit: 5e1dac80 (official source into the production baseline).
- hloolx source: 2caf579bc1e47905f5260ccfa3982b586a6d76a0.
- Integration branch: codex/update-hloolx-official-20261016.
- Worktree: codex2api-update-20261016/codex2api.
- This work changes and tests source only. It does not deploy, push, or modify production services/databases.

## Conflict resolution and feature preservation

Resolved 15 conflicted files for the official merge and 31 for hloolx. Each conflicted hunk was examined; no repository-wide ours/theirs replacement was used.

- Admin service fields, registration, startup and shutdown now include production account operations, smart operations, credential operations, token guard, plugins, State pool/IPv6 State, official intelligence checks, and hloolx model-quality guard. Existing execution interfaces and worker routes remain.
- Kept production Basispoints selection, preparation diagnostics, owner isolation, tool transport and streaming behavior; integrated official Responses compatibility, native relay WS selection, multiplexing and continuation recovery. Native WebSocket relay filtering uses the new official relay-aware filter under the production Codex route filter.
- Carried hloolx dedicated host IPv6 HTTP/OAuth/WS transport isolation and bounded pre-output retry semantics into the official WS connection-dial/capacity/continuation refactor. Kept model, owner, client identity, target URL and egress checks, plus IPv6-route continuity. Rotated IPv6 never reuses a connection from its old address.
- Preserved explicit account-bound egress over Resin/global/IPv6 inheritance. Executor-level WebSocket URL resolution remains context-aware for bound requests; dedicated IPv6 acquisition is not duplicated for ordinary traffic.
- Preserved verified-State ordinary direct egress. The explicit State route bypasses dedicated IPv6 allocation in both the executor wrapper and request egress resolver.
- Model cooldown/quality filtering remains context-aware. Once a Codex route decision exists, the global native-State filter is not applied again; State is checked per eligible path, preserving per-account quality BPS recovery while global BPS is disabled. The WS route decision is established before this filter.
- Preserved production single-machine/multi-window identity convergence and its block on custom headers that would override the generated identity. Kept custom-header identity behavior when convergence is off and official maintenance unified identity controls.
- Kept the fingerprint usage column across insert, export and paged reads while adopting hourly rollups, inserted-id returns and paged-id queries. System setting SQL parameters independently persist BPS, unified identity, downstream keepalive slots and prompt-review preservation flags.
- Updated production smartops/State/quality probes and their tests for upstream typed atomic counters; no scheduler behavior removed.
- Added upstream model-quality and dedicated IPv6 UI/API fields and translations alongside production pages. Kept the hloolx explicit connection-test start/prompt/version-sync/detector workflow and official Antigravity disabled-model selection rules.
- Preserved State and route browser CI jobs and Redis race coverage; combined PostgreSQL CI test selections for production routes/probes, intelligence, model quality, IPv6 and unified identity.
- Removed only duplicate declarations introduced by additive merge of equivalent Turn State helpers/fields and duplicate TypeScript type property. Existing credential runtime source and dependency pins remain unchanged.

## Verification adjustments

- The first Go 1.26.9 download from the default proxy timed out. Exploratory tests used installed Go 1.27.2; its default JSON v2 changed parser diagnostic strings. GOEXPERIMENT=nojsonv2 confirmed the BPS behavior was compatible. Final verification uses the exact downloaded Go 1.26.9 toolchain, with no JSON experiment override.
- Import unit fixtures stub usage probes but were triggering the production default-on invite probe using synthetic credentials. Their dedicated test DB now explicitly disables this unrelated feature. Production defaults and import functionality are unchanged.
- The new upstream intelligence TLS/CONNECT fixture now covers four cases: configured HTTP identity, updated HTTP config, generated single-machine WS identity, and custom WS identity with convergence off. The proxy destination assertion remains strict; the test never sends credentials outside its local fixture.
- Intelligence browser mock exports the retained smartops concurrency API. The detail-sheet test reflects production's single detail-sheet opening instead of pressing Escape to close a nonexistent second sheet.
- PostgreSQL trace fixtures now use an account-specific request ID, allowing safe repetition in the disposable test database. Fingerprint and combined-setting assertions were strengthened.

## Results

- Frontend typecheck: PASS.
- Frontend unit tests: PASS, 431 tests.
- Frontend production build: PASS; existing large-chunk advisory remains.
- Intelligence browser suite: PASS, 7 tests, including desktop/mobile/history/detail/save failures.
- Credential runtime Python tests: PASS, 8 tests.
- PostgreSQL 18 disposable database targeted race suite: PASS (114.237 seconds). Includes routes/probe fencing, trace/capabilities, model quality, IPv6, intelligence and identity setting migrations.
- PostgreSQL strengthened settings/fingerprint follow-up: PASS (13.516 seconds).
- Focused routing race regressions: PASS (proxy and wsrelay). Covers route-aware strict State admission, quality BPS transport, enabled-IPv6 State-direct bypass, rotated IPv6 continuation isolation, account-bound egress and pre-output retry boundaries.
- Final complete Go 1.26.9 suite: PASS, all 26 tested packages (plus three packages without tests), with -count=1 -timeout 240s.
- Synthetic State browser suite: PASS, dashboard/accounts/renewal/settings/saved filters/deselection on desktop/mobile in light and dark themes. Codex route browser interactions: PASS on desktop and mobile.
- Source review: three actionable merge regressions were corrected (account-bound WS URL resolution, State-direct/IPv6 precedence, route-aware State filtering). Read-only recheck accepted the corrections.

## Legacy-table audit

No Go runtime or SQL source references were reintroduced for:

- codex_turn_state_renewal_history
- codex_turn_state_renewal_routes
- codex_turn_state_renewals
- codex_turn_state_templates
- image_relay_lock
- image_relay_reservations

The legacy deploy/release/release_host.py still lists the two image relay tables in an old release expectation. It is not used by this in-place release controller. No database deletion is performed by this integration work. State, smartops, credential operations, maintenance_jobs and scheduler_outbox storage remains active and preserved.
