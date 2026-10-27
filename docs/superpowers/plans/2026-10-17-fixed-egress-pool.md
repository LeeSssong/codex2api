# Fixed Egress Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: subagent-driven-development. Execute sequential scoped tasks with independent review.

**Goal:** Deliver and deploy durable account egress pools sourced from CliProxy API and manual static proxies.
**Architecture:** Additive database resources/bindings + fixedegress manager/provider + shared Codex route integration. Explicit enrollment and default-off configuration preserve current production behavior.
**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, Docker Compose.
**Spec:** docs/superpowers/specs/2026-10-17-fixed-egress-pool.md

## Global Constraints
- User approved implementation and deployment; existing checkout, new codex/ branch, no worktree.
- Preserve unrelated untracked files, all ordinary accounts/keys/concurrency, and unrelated services.
- No real provider key supplied: no paid external start/stop requests, production remains disabled until configured.
- Bindings persist; no healthy rebalancing; exclusive observed exit IP; fail closed; old failure generation fencing.
- OAuth/visible output never blindly replayed; bounded retries; local IPv6/Resin conflicts rejected.
- Never issue stop-all or blind ambiguous start retries; secrets masked and protected.

### Task 1: Backend feature
**Files:** database/fixed_egress*.go, fixedegress/*.go, admin/fixed_egress*.go, main.go, existing admin route registration, auth/proxy integration, proxy/wsrelay integration, existing conflict setters.
**Interfaces:** Defines /api/admin/fixed-egress endpoints and documents exact frontend JSON contract in docs/fixed-egress-api.md. Manager resolves account binding before all native Codex identity-bearing traffic. DB shares resources across instances, transactions fence assignment/rotation; persisted provider tasks fence start.
- [ ] Write behavioral database/provider tests and observe RED: restart durability, no rebalance, unique observed exit, stale rotate, strict/exhaustion, parsing/auth/online failure/start ambiguity/stop ownership.
- [ ] Implement additive SQLite/PostgreSQL schema, transactional resource/enrollment/binding/event APIs, validated secret storage.
- [ ] Implement provider management with bounded HTTP parsing and admin validation; fixture tests prove online never starts/stops and ambiguous tasks reconcile.
- [ ] Write integration tests for HTTP/WS/OAuth/maintenance routing/fail closed/replay and incompatible settings, then integrate manager at existing route boundaries.
- [ ] Run go test on changed packages then entire suite; record commands/evidence, commit backend and contract.

### Task 2: Backend task review
- [ ] Generate base-to-head review package; independent reviewer checks spec compliance AND quality.
- [ ] Resume implementer to fix material findings and test covering paths; independent scoped re-review.

### Task 3: Admin frontend and usage guide
**Files:** frontend/src/components/FixedEgressPanel*.tsx, frontend/src/lib/fixedEgress*, frontend/src/api.ts, frontend/src/pages/Proxies.tsx, locale entries, frontend/tests/fixed-egress.browser.test.mjs, docs/fixed-egress.md.
**Interfaces:** Consume backend contract from Task 1; use existing UI controls and admin auth; no provider key echoed or accidentally cleared on edit.
- [ ] Add functional panel: pool/provider editing, imports, tests, online/start, explicit owned stop, account enrollment/unenrollment/manual rotation, binding/event status; validate drafts and preserve errors on refresh.
- [ ] Write meaningful behavioral tests for drafts, redaction, parsing and actions; exercise desktop/mobile UI using local fixture server.
- [ ] Run typecheck, unit tests and production build; document actual controls/defaults/compatibility, commit.
- [ ] Independent scoped frontend review; fix findings through implementer and re-review.

### Task 4: Integration and deployment
**Files:** docs deployment evidence, deploy/release targeted additive-release tooling if needed.
- [ ] Verify complete Go suite, targeted race/concurrency tests, SQLite and PostgreSQL additive migration, frontend checks and browser flows, git diff --check.
- [ ] Broad final code review of feature range, fix material findings and verify affected paths.
- [ ] Commit reviewed source, build immutable amd64 image from git archive, record revision/tree/build version.
- [ ] Inspect production topology and compose, back up PostgreSQL and compose, deploy only Codex app with additive migration and rollback image.
- [ ] Verify public health/version, authenticated fixed-egress endpoints/default-off state, retained APIs and unrelated container identities. Record rollback/configuration path and source provenance.
