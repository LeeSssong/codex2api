# Independent Codex2API Smart Operations Implementation Plan

**Goal:** Integrate upstream, port Sub2API 2FA and all seven smart operations capabilities into independently managed Codex2API plugins, then deploy only Codex2API.
**Architecture:** Codex2API owns all code, accounts, data, credentials, workers and releases. Port source behavior with native Go/database/React adapters. No calls to Sub2API services, databases or worker. Request-path policies remain local; the login worker is an independently versioned Codex2API runtime.
**Authorization:** User approved implementation and production deployment on 2026-10-03. Sub2API must remain serving throughout.
**Source:** Production/origin main 93d33210; upstream 7d717d7f. Older e557d171 contains previously verified smart operations source that was removed by the intervening upstream replacement.

## Constraints
- Preserve the three existing uncommitted changes in the original checkout.
- One writer per worktree. Implementers own independent worktrees; controller integrates commits.
- No Sub2API mutations, shared credentials, data, jobs, business service or plugin center.
- Plugin flags must gate backend tasks and mutations, not just hide menus. Preserve histories when disabled.
- New password/TOTP persistence requires encryption; public responses and logs must never expose secrets.
- Reuse existing native account identity, scheduler eligibility, credential publication and billing.
- Build only from clean pushed non-detached main matching verified remote; retain rollback artifacts.
- Schema deployment affects Codex2API only, drains old requests up to 300 seconds and verifies backup restoration before migration.

## Tasks
- [x] 1. Integrate latest upstream using ours for conflicting hunks, retain upstream ancestry, inspect deletion/interface compatibility, and validate affected Go packages.
- [x] 2. Restore and adapt accountops/tokenguard, native database jobs, observer hooks and existing React pages from e557d171. Verify quality rules, alert classification, credential generations, leases and module off behavior. Introduce a native plugin registry with per-capability version/flags; preserve legacy routes.
- [x] 3. Port initial 2FA import and credential operations V2, encryption gating, account identity deduplication, asynchronous login/cancel/status, probe/relogin cooldown and configurable engine/proxy controls. Supply a Codex2API-owned login runtime and image; no Sub2API endpoint defaults. Test native SQLite/PostgreSQL persistence and synthetic login protocol failures/success.
- [x] 4. Port auto configuration, priority scheduling and group scheduled Pelican tests. Cover account creation defaults/model mapping/BPS/concurrency progression; eligibility-preserving local scoring; leased tests through the real scheduler with cost/history/display. Write directly relevant tests before new behavior.
- [x] 5. Integrate seven React views and account 2FA import, plugins enable/disable controls, API types and localized copy. Build/typecheck and desktop/mobile browser validation with synthetic data. Display no secrets.
- [x] 6. Review integrated behavior and independence. Run relevant Go, SQLite/PostgreSQL and frontend/browser checks. Document source SHAs, plugin capabilities, upgrade boundaries, actual test evidence and remaining live login limitations.
- [x] 7. Adapt release script for current actual protected containers, minimal feature smoke and Codex-only worker/runtime. Verify migration rehearsal/restoration; commit/push/integrate main preserving original user changes. Build once, upload and deploy Codex only. Compare Sub2API container identity/start time and health before/throughout/after. Record one release report with digest, source, elapsed stages and rollback entry.

## Progress
2026-10-03: Read target production state and deployment controller. Origin main is ahead of original local main. Created independent-plugins worktree from origin/main. Upstream merge staged successfully; no unresolved conflicts. Sub2API healthy and running; no production mutations performed.

- Upstream merged as a7517e92. Actual production already incorporates upstream through 56015088; 14 additional upstream commits were merged. Proxy and prompt-filter tests passed; admin compile revealed a missing internal/version import, assigned to the core restoration task. Frontend baseline typecheck and 380 tests passed.
- Release protection commits 89df4af2 and d1fbdb15: source gate uses actual origin, all unrelated running container IDs and StartedAt captured, Caddy configuration changes outside the Codex route rejected. 38 scoped release tests pass.
- Independent worktrees assigned: plugins-core (restore/register/wire), plugins-credentials (2FA/V2/runtime), plugins-policies (configuration/ranking/group tests). Root retains integration/release ownership. No writes to Sub2API checkout or services.
- Production preserved prior accountops/quality/tokenguard tables. Original checkout still has exactly its three original uncommitted paths.
- Local Docker storage is full; no existing images/volumes were pruned. Disposable PostgreSQL test container uses tmpfs, localhost port 15439; databases core_test/credentials_test/policies_test are separate. Runtime artifact packaging may use a dedicated resource-limited Codex build worker on the production host, with compilation and frontend build local.
- Policy task initial delivery lacked live host hooks; returned to implementer for complete account creation, success progression, actual scheduler and durable billed group-test integration. Utility-only output is not accepted as task completion.
- Restored existing accountops/tokenguard backend and pages in d98682b7/5c5cd239. Root verified plugins/admin/accountops/tokenguard tests pass. Further review found observer unconsumed and plugin gates coupled; assigned corrections to original owner before acceptance.
- Plugin management page b17d3811 uses existing shared UI controls and localized messages, manifest versions display actual installed metadata, with compiled vs independently updated runtime boundaries. Typecheck passed before restored pages integration; full shell wiring pending.
- Release packaging 7b9c8e96 can use explicitly selected dedicated remote builder while keeping source compilation local. 41 release tests pass. No build daemon or production service has been mutated yet.
- PostgreSQL broad baseline run failed TestImageUserBillingPersistencePostgres due prior test rows (logs=3). Isolated fresh image_billing_isolated database passed that exact test. Feature PG tests use independent module databases.
- Replacement implementers policies_real_integration and credentials_real_integration (gpt-6.1-sol, high) own completion after earlier scaffolds failed review. Credentials initial fake success was rejected and never deployed. Pending durable implementation must use verified login before native import, token leases/cancellation/generation fences and actual worker protocol.

- Final integrated validation: remaining full Go packages passed in the initial integrated run; four native fixture failures corrected in80e75b09 and full affected admin/database/smartops rerun passed (78.465s/6.622s/1.718s). Native quality evidence test additionally confirmed PASS with verbose output, not skipped. Frontend typecheck,392unit tests and final build pass; Python worker8tests pass; release54tests pass. Relevant SQLite/PostgreSQL and race evidence supplied by implementers, core off/on/group invariants independently rerun by root. Real local HTTP all plugin endpoints200, first2FA queue/cancel creates0nativeaccounts, auto-config save confirmed via API. Desktop/mobile2FA390px no horizontal overflow; dialog body scroll verified. Screenshots .deploy/independent-plugins-ui outsideGit.
- Independent final reviewer found and verified fixes for monitor stale config/cancel, BPS recovery ABA epoch, obsolete alert/token trigger coupling, cross-channel BPS group movement, transactional policy gates/billing snapshots and stale scheduled-plan dispatch. Final source reviewed2ef4f138+fixture84ad9e73; additional tiny native UIlabel/default changes verified by actual browser and build. No open Critical/Important findings after fixes.
- Official source pinned08030dd23f438e0e179afb9140ebf8d4c5d3345e. Unsupported native Mihomo and BPS WebSocket acceleration explicitly unavailable/rejected; supported local IP-pool/standard BPS SSE options have real consumers. Live OpenAI credentials/real email delivery/external Session Studio not exercised; synthetic and pinned-engine mock evidence only.
