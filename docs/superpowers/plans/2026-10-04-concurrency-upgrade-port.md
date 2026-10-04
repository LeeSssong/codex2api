# Concurrency upgrade parity implementation plan

> Execute approved work in this session with independent agents for database/state rules, runtime, and interface. Root owns request instrumentation, integration, and release.

**Goal:** Bring existing Codex2API progression to the approved Sub2API semantics and release it with the latest official ancestor.

**Architecture:** Reuse smartops OAuthAutoConfig and smart_ops_concurrency. A request records outcomes once per account through a bounded auth queue; database transactions fence configuration revision, manual concurrency edits, plugin epoch and account eligibility. Existing native health/quota guards still decide actual admission.

**Tech stack:** Go, SQLite/PostgreSQL, React, existing Python blue/green release.

**Source:** /Users/gongtengxinwen/Documents/sub2api搭建/upstream/sub2api/docs/account-auto-config.md:64; approved conversation design.

## Contracts

Add smartops.ConcurrencyObservation with AccountID int64, Success bool, StartedAt time.Time, Revision string, Epoch int64, CurrentConcurrency int64, Reset bool. Database adds RecordSmartOpsConcurrencyObservation(context.Context, smartops.ConcurrencyObservation, smartops.OAuthAutoConfig) (int64, bool, error), preserving the legacy RecordSmartOpsConcurrency wrapper. smartops.ConcurrencyState uses explicit lower-case JSON fields and reads legacy capitalized fields.

Auth exports BeginSmartOpsConcurrencyObservation(account *Account) smartops.ConcurrencyObservation and ReportSmartOpsConcurrencyObservation(observation smartops.ConcurrencyObservation). Status is SmartOpsConcurrencyStatus() (bool,string). Proxy gathers actual per-account HTTP outcomes and submits once per request; failure wins over retries on the same account, cancellation gives no success. Probes, WS, images, count-tokens and compact excluded.

Admin GET /api/admin/smart-ops/concurrency-progress?ids=1,2 returns {enabled,paused,reason,progress:{"1":{revision,concurrency,successes,required,maximum,step,paused_until}}}; bound ids to 200 and apply authenticated admin access. Reuse native config endpoint for settings. Unknown/disabled state hides account progress.

## Tasks and checks

- [x] Rules/database: regression tests for promotion cooldown, request-start fencing, max cap, disabled/unavailable accounts, rule/manual edit fences, legacy state decode, transaction consistency and quality recovery coexistence; implement, run targeted smartops/database tests.
- [x] Auth runtime: regression tests for nonblocking queue, saturation/storage-failure pause until new config revision, bounded processing context and restart reset; implement methods above, remove broad ReportRequestSuccess/Failure progression side effects, run auth tests.
- [x] UI/admin: settings show paused state, account list shows progress using batch API; keep approved layout and existing translations; test API validation and projection, typecheck/build frontend.
- [x] Proxy: tests for complete success, failures dominate retries, client cancellation, incomplete streams and exclusion scopes; instrument HTTP text outcomes through usage finalization and actual attempt acquisition; run proxy tests.
- [x] Integration: run Go suite, frontend tests/typecheck/build and release-controller tests; fix concrete failures.
- [ ] Release: verify official origin/main remains an ancestor; commit and push custom production main; build immutable linux/amd64 image with revision/tree labels from clean source; deploy via blue/green; verify health/version, admin config/progress and unrelated container invariance; document release evidence.
