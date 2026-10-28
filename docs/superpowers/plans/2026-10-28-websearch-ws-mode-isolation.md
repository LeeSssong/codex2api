# WebSocket Responses Lite mode isolation Implementation Plan

> **For agentic workers:** Execute inline with executing-plans and test-driven-development. No deployment, push, main merge, production tests or additional agents are authorized by this implementation request. Steps use checkbox syntax for tracking.

**Goal:** Prevent hosted web search from reusing an upstream WebSocket already routed to Responses Lite, without dropping tools or conversation history.

**Architecture:** Treat the effective, finalized per-frame Lite marker as part of the local transport identity. Scope both stateless reusable pools and explicit-session lanes by that identity; record it immutably when dialing so previous_response_id acquisition can enforce the same identity. Carry the mode through a private Go context value, never an extra upstream header or a prompt/session rewrite. A mode-mismatched continuation must use the existing typed continuation-lost/full-history-recovery path, not send an unresolved ID to a new connection.

**Tech Stack:** Go 1.26.10, gorilla/websocket, existing manager/lease/capacity machinery, local httptest upstreams.

**Spec:** User-approved explanation in this chat: isolate upstream Lite/standard connections, not accounts, visible chat IDs or stored history. Diagnosis retained outside Git at `/Users/gongtengxinwen/.config/codex2api/investigations/20261028-websearch-ws/REPORT.md`.

## Global Constraints

- Owner: this chat; base `d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1` from fetched origin/main.
- Worktree: `/Users/gongtengxinwen/.codex/worktrees/websearch-ws-mode-isolation/codex2api`; branch `codex/websearch-ws-mode-isolation`.
- Original shared checkout is preserved and must not be edited.
- Local implementation/testing only. Do not deploy, restart, connect to production, change settings/accounts, or use a remote Docker context.
- No database/schema/frontend/monitor changes; retain tools and effective model/Lite gate behavior.
- Standard mode must keep existing transport keys/identity to minimize unrelated churn. Lite adds a local protocol scope; same-mode reuse remains enabled.
- Do not silently remove previous_response_id, encrypted history, web_search or a Lite marker to make a request pass.

## Task 1: Reproduce and isolate mode-aware connection acquisition

**Files:** create `proxy/wsrelay/responses_lite_mode.go` and `proxy/wsrelay/responses_lite_mode_test.go`; modify `proxy/wsrelay/executor.go`, `proxy/wsrelay/connection_dial.go`; correct stale comment in `proxy/executor.go`.

**Interfaces:** existing `ExecuteRequestViaWebsocket` consumes finalized WS body; `withWebsocketResponsesLiteMode(ctx, body)` carries bool only locally; `websocketTransportIdentity(headers, lite)` and `websocketTransportPoolKey(key, headers, lite)` produce stable local identity; wrapDialedConnection records dial-time mode in upstreamClientIdentity. Existing continuation APIs compare that identity unchanged.

- [x] Baseline `go test ./proxy/wsrelay ./proxy -count=1` — both packages passed before changes.
- [x] Add loopback sticky-Lite fake upstream: once a connection sees Lite, a subsequent standard hosted web_search frame returns the exact rustponsesapi error. Test both Lite→standard and standard→Lite→standard for explicit and stateless pools, checking successful terminal/search events, same-mode reuse, unchanged application/frame identity and no leaked local scope.
- [x] Run new tests on baseline — failed with the reproduced rustponsesapi 400 and cross-mode stale continuation.
- [x] Implement private context mode and protocol-aware local identity/keys. Update continuation identity and immutable dial-time identity. Keep retry and IPv6 paths using the same context/final body.
- [x] Run targeted tests to green.

## Task 2: Verify continuation safety and retries

**Files:** `proxy/wsrelay/responses_lite_mode_test.go`, existing continuation/retry test fixtures if needed.

**Interfaces:** existing typed `ResponsesContinuationLostError`, response binding and pending read leases; no new public error/API.

- [x] Test same-mode previous_response_id reuses and succeeds on its original connection.
- [x] Test both cross-mode continuations refuse before sending any frame or dialing a replacement, retaining original binding, lease health and same-mode recovery. Keep caller's original body intact.
- [x] Inspect existing higher-level full-history replay: retain history and route-protocol conflict protection rather than stripping IDs blindly.
- [x] Verify mode survives send-redial, overflow slots and replay/replacement paths using local tests or focused context/dial tests.
- [x] Review complete diff for bypasses (stateless, explicit, continuation, redial, IPv6), leaks and races.

## Task 3: Verification and local handoff

**Files:** this plan, `TASK-CONTEXT.md`, `docs/collaboration.md`, implementation/test paths above.

- [x] Run focused mode tests repeatedly with race detector, then full wsrelay/proxy tests. `go test ./...` reached all relevant packages but had one unrelated first-run `credentialops` timing failure; isolated rerun passed.
- [x] Run `npm ci && npm run build`, `go build ./...`, `git diff --check`; origin/main remained `d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1` and shared checkout was not edited.
- [x] Update checklist and handoff with exact commands/results, limitations and explicit NOT DEPLOYED status.
- [ ] Commit only owned paths on the isolated branch if verification succeeds; do not push, merge or deploy.
