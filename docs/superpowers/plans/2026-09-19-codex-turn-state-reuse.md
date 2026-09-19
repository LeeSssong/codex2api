# Codex Turn-State Reuse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add account-scoped reusable `X-Codex-Turn-State` harvesting and injection to codex2api without changing existing continuation behavior outside in-scope Astra requests.

**Architecture:** A new `internal/turnstate` package owns ticket validation, keying, storage, lifecycle state, and outbound policy. The existing provenance guard remains unchanged; HTTP/WS executors invoke the new overlay only after normal header filtering. A background harvester writes qualified tickets and a small admin surface controls settings, group scope, and status.

**Tech Stack:** Go, PostgreSQL/SQLite, Redis-compatible cache, React/TypeScript, existing admin API and scheduler.

**Spec:** `docs/superpowers/specs/2026-09-20-codex-turn-state-reuse-design.md`

## Global Constraints

- Feature and group switches default off; miss/recovered actions default `none`.
- Only OAuth Codex accounts in at least one enabled Codex group are in scope.
- Only effective `gpt-6-astra` ordinary `/responses` requests may be overwritten.
- Compact, non-Astra, non-generation, relay, API-key, and Agent Identity paths remain byte-for-byte unchanged for turn-state handling.
- Valid lengths are exactly 292/332 decoded as 217/249 bytes; TTL is 3570s and renewal begins at 3000s.
- Request paths never wait for harvesting and storage/injection failures fail open.
- Existing incoming continuation signals remain the sole WHAM/sticky continuation evidence.

---

### Task 1: Ticket Core and Store

**Files:**
- Create: `internal/turnstate/ticket.go`
- Create: `internal/turnstate/store.go`
- Create: `internal/turnstate/policy.go`
- Test: `internal/turnstate/*_test.go`

**Interfaces:**
- Produces: `Parse(raw string, now time.Time) (Ticket, error)`, `CredentialHash(accessToken, accountID string) string`, `Key{AccountID int64, Model, CredentialHash string}`, `Store.Get/Put/Delete`, `ApplyOutbound(DecisionInput) (ticket string, applied bool)`.

- [ ] Write parser tests for 292/332 acceptance, 312/356 rejection, malformed base64/header/shape, expiry, and future clock tolerance; run `go test ./internal/turnstate -run TestParse -count=1` and confirm RED.
- [ ] Implement parser/key/status values and rerun the parser suite GREEN.
- [ ] Write store tests proving credential rotation isolation, atomic newer-ticket replacement, stale response protection, Redis-read failure as miss, and valid local fallback rules; confirm RED.
- [ ] Implement the cache-backed store with an in-memory validated-ticket mirror and rerun `go test ./internal/turnstate -count=1` GREEN.
- [ ] Commit with `git commit -m "feat(turnstate): add reusable ticket core"`.

### Task 2: Settings, Group Scope, and Status Projection

**Files:**
- Modify: `database/postgres.go`, `database/sqlite.go`, `database/account_groups.go`
- Modify: `admin/handler.go`, `frontend/src/types.ts`
- Test: `database/sqlite_test.go`, `database/account_groups_test.go`, `admin/handler_test.go`

**Interfaces:**
- Produces: `SystemSettings` turn-state fields, `AccountGroup.TurnStateInjectEnabled`, scope queries, and `GET /api/admin/turn-state/status` DTOs without ticket contents.

- [ ] Add failing persistence/default/scope/status tests; run focused database/admin tests and confirm RED.
- [ ] Add additive PostgreSQL/SQLite columns and settings serialization with validation for action enums and target groups.
- [ ] Add group create/update/list support and a scope query that requires Codex channel plus enabled membership.
- [ ] Add redacted account status response and rerun focused tests GREEN.
- [ ] Commit with `git commit -m "feat(turnstate): persist reuse settings and scope"`.

### Task 3: HTTP, WebSocket, and Scheduling Overlay

**Files:**
- Modify: `proxy/executor.go`, `proxy/wsrelay/executor.go`, `proxy/handler.go`, `proxy/responses_ws.go`
- Modify: `proxy/codex_turn_state.go`
- Test: `proxy/executor_test.go`, `proxy/wsrelay/executor_test.go`, `proxy/responses_turn_state_test.go`, `auth/store_scheduler_test.go`

**Interfaces:**
- Consumes: `turnstate.Store`, scope/settings provider.
- Produces: per-attempt `ApplyOutbound` after existing guard/header filtering and a scheduler eligibility predicate for non-`none` misses.

- [ ] Add failing tests for Astra overwrite, Sol/Terra/compact/no-ticket-none unchanged behavior, failover account isolation, and outgoing injection not affecting WHAM continuation classification.
- [ ] Wire HTTP overlay immediately before `Do`, preserving `codexTurnContinuationToken` computed from inbound data.
- [ ] Wire WS `response.create.client_metadata.x-codex-turn-state` overlay using the current attempt account only.
- [ ] Add miss-action scheduler exclusion without fallback-through and rerun focused proxy/auth tests GREEN.
- [ ] Commit with `git commit -m "feat(turnstate): apply account tickets on Astra requests"`.

### Task 4: Harvester and State Transitions

**Files:**
- Create: `admin/turn_state_harvester.go`
- Modify: `main.go`, `auth/store.go`, `database/account_groups.go`
- Test: `admin/turn_state_harvester_test.go`, `auth/store_scheduler_test.go`

**Interfaces:**
- Produces: `StartTurnStateHarvester(context.Context)`, bounded concurrency 3, per-account lease, rotating harvest routes, two-call qualification, and one-shot missing/recovered actions.

- [ ] Add failing httptest-based cases for complete SSE + Astra + 292/332, revalidation, 429 retention/backoff, 401 current-hash deletion, incomplete streams, renewal failure retaining old ticket, and no route.
- [ ] Implement collector request/response classification, route rotation, lease, cadence, and atomic ticket publish.
- [ ] Implement miss/recovery transition actions and clearing only `turn_state_miss` suspension state.
- [ ] Start/stop worker from `main.go`; rerun admin/auth tests GREEN.
- [ ] Commit with `git commit -m "feat(turnstate): harvest reusable Astra tickets"`.

### Task 5: Admin UI and Localization

**Files:**
- Modify: `frontend/src/pages/Settings.tsx`, `frontend/src/components/AccountGroupManagerModal.tsx`, `frontend/src/pages/Accounts.tsx`, `frontend/src/types.ts`
- Modify: existing zh-CN/en/ja locale files
- Test: related Vitest suites

- [ ] Add failing component/type tests for defaults, Codex-only group toggle, action target requirements, and redacted account status display.
- [ ] Add a Codex Turn-State settings section, group switch, account status/summary, and three-language strings using existing controls.
- [ ] Run `npm test -- --runInBand` for touched suites and `npm run build` GREEN.
- [ ] Commit with `git commit -m "feat(turnstate): add reuse administration UI"`.

### Task 6: Verification and Release Candidate

- [ ] Run `gofmt`, `git diff --check`, focused Go tests, `go test ./...`, frontend tests, and production build.
- [ ] Verify total switch off stops harvest/injection and restores only turn-state-miss suspensions.
- [ ] Commit the spec/plan and any final fixes; push `codex/turn-state-reuse`.
- [ ] Record commit/tree, tests, and deployment dependency on the sub2api migration release.
