# Account IPv6 selection and New York import defaults Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Deploy account-opted IPv6 routing with actual bound IP display, correct opt-out and New York import default.
**Architecture:** Store account membership separately from proxy URL, reuse unique IPv6 bindings and rotation, reconcile selection at account configuration boundaries. Dedicated host ipvlan attach service ensures pool visibility without touching other services.
**Tech Stack:** Go, React/TypeScript, PostgreSQL/SQLite tests, Python host deployment.
**Spec:** docs/superpowers/specs/2026-10-29-account-ipv6.md

## Global Constraints
- Only codex2api deployment; no Sub2API/database/Redis restart. Preserve existing account enablement, worker settings and credential runtime.
- No development in historical shared dirty root. Release only clean pushed root main; expected live identity and release lock required.
- API contract: `ipv6_egress_enabled` boolean on account response/add/import/scheduler, `ipv6_egress_ip` read-only response. Import `default_timezone` omitted=America/New_York, explicit empty=no default.

### Task 1: Backend membership, lifecycle and defaults
Files: auth/, database/ipv6_egress*, egressipv6/, proxy/ipv6_egress* and OAuth/WS routing, admin account creation/import/update/response.
- [x] Write/run focused failing tests for eligibility, exclusive claim, non-member bypass, disabled pool, selection removal, source defaults and explicit preservation.
- [x] Implement membership and default timezone while preserving explicit source settings and duplicate preferences. Keep account proxy URLs real.
- [x] Verify HTTP/WS/OAuth/maintenance/telemetry route selection and retirement concurrency; run affected package tests.
- [x] Commit only owned backend files and report API details/validation.

### Task 2: Frontend selection and import controls
Files: frontend/ only. Consumes Task 1 API contract above.
- [x] Add Local IPv6 selection, actual IP/pending display, correct payload clearing on proxy/blank, appropriate testing action.
- [x] Default native account add/import to selected IPv6; add checked New York default control and preserve explicit source data.
- [x] Add direct behavior tests, typecheck/build and browser smoke as appropriate; commit frontend files.

### Task 3: Persistent pool and deployment integration
Files: deploy/release/, new deploy IPv6 helper/service and tests, docs/ipv6-egress.md.
- [x] Inspect exact live compose/image/binary/source and snapshot protected container identities.
- [x] Implement idempotent attach plus startup/recreation reconciliation with identity safeguards, no unrelated routes/network daemon changes.
- [x] Test attach command generation/idempotence/failure rollback and integrate readiness with maintenance deploy and rollback.

### Task 4: Integration and production
- [x] Integrate tasks; focused spec/quality review, relevant regression tests; fix actionable findings.
- [x] Merge into clean root main, push origin/main, verify fetched commit/tree, build once and transfer pinned artifact.
- [x] Notify maintenance scope, acquire lock, recheck live identity, rehearsal and maintenance deployment only codex2api. Verify pool before enablement.
- [x] Real supported-model IPv6 request, account API/state and import defaults checks; verify other service identities unchanged.
- [x] Record source/tree/image, tests, timings, rollback and limitations in one release note/TASK-CONTEXT.

### Additional user-authorized scope: reported client snapshot
- [x] Port the reported-client feature from `.worktrees/codex-reported-client/` into current main-compatible code without copying its old source tree. Snapshot has no Git metadata and contains unrelated historical changes.
- [x] Prefer recognized client kind from official daily usage; missing, unknown or failed history keeps existing pool selection. Preserve current client-version validation, transport and IPv6 routing.
- [x] Validate behavior and race checks; integrate this feature into the same clean-main artifact and deployment.

### Original user requirement reconciliation: manual pool address selection
- [x] Add atomic account-scoped manual IPv6 selection with expected-current fence, occupied/cooldown checks, and old lease drain; never change enabled state.
- [x] Add available-address picker in main and quick editors for a saved Local IPv6 selection, including pending accounts; refresh actual binding after success.
- [x] Verify conflict, occupied/cooldown, active connection retirement and frontend request/state behavior before including in the same release.

Production completed: see `docs/releases/2026-10-29-account-ipv6.md`. All task branches are historical merged evidence.
