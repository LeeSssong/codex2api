# Codex OAuth dual upstream routing

Local implementation based on `bps-opt`, baseline
`d550fdc9933c63dbbcc466275b7357355e6021c9`. This describes local code, not a
production deployment. Tests use synthetic credentials and mock upstreams.

## Decision and configuration

One account keeps its existing ID, credentials, groups, quota, concurrency,
affinity and accounting. Basispoints is an internal Codex OAuth path.

| Dimension | Meaning |
| --- | --- |
| Administration | Per-account/path permission; missing record means allowed |
| Evidence | Account/path/exact effective model: unknown, supported, unsupported; source/reason/request-start timestamp |
| Health | Process-local path/model cooldown and exclusive recovery probe |
| Request permission | Existing Key channel, groups, no-affinity split, plans, model permissions, quotas and new capability filter |

Order: validate and apply existing explicit model mappings; resolve policy;
compose filters in the existing scheduler; lease one account; choose its first
eligible path; recheck path permission, exact-model evidence, health and State
before each attempt; rebuild from canonical input; switch at most once.
Preference applies inside the selected account, not as a new pool scheduler.
The controlled access-failure branch does not scan other accounts for permission.
Other existing retries remain on the final selected path.

Policy precedence: Key `limits.codex_route_policy` -> `CODEX_ROUTE_POLICY` ->
compatible model default (BPS-prefer for enabled allowlisted models, native-only
otherwise). Omitted/`inherit` follows the next level. Invalid deployment policy
fails closed. Environment changes require restart.

| Policy | Allowed path order |
| --- | --- |
| `codex_only` | Native only |
| `basispoints_only` | BPS only |
| `basispoints_prefer` | BPS, then native when `BASISPOINTS_NATIVE_FALLBACK` permits |
| `codex_prefer` | Native, then BPS when all BPS gates permit |

Runtime `CodexBasispointsEnabled` is always a hard gate. No Key can enable it.
`BASISPOINTS_MODELS` defaults to `gpt-5.6-sol,gpt-6-astra`; matches are exact,
case-insensitive, or valid `YYYY-MM-DD` snapshots. Near-prefix names and invalid
dates do not match. `*`/`all` expands models, not permissions or evidence.
`BASISPOINTS_NATIVE_FALLBACK=false` removes native from BPS-prefer, including
image/protocol fallback. Explicit native-only/native-prefer still permit native.
Agent-identity credentials are not eligible for BPS.

`limits.codex_capability_filter`: omitted/`any` permits unknown; `supported`
requires the selected path's support; `dual_supported` requires both paths;
`codex_supported` / `basispoints_supported` requires that path's evidence.
All are ANDed with actual-path permission, health and unsupported exclusion.
Family evidence does not establish snapshot support.

Existing NoAffinityGroupIDs semantics remain: without a recognized engine
fingerprint or `X-Codex2API-Affinity-Key`, configured split groups are used;
fingerprinted requests use ordinary allowed groups (or exclude split groups
when unrestricted). A session ID alone is not this fingerprint. Both attempts
use the same leased account; no new authorization entry point or group mutation
is introduced. This existing client-fingerprint behavior is not a security wall.

`CodexRouteDecision` records requested/effective model, Key groups, preferred/
allowed/final paths, account/path/model attempts, actual HTTP status, separately
reported status, code/source/reason and remaining budget. Ordinary, encryption,
continuation and path attempts share `min(32, 3 + general retries + rate retries)`
(nonnegative retries); continuous/unlimited policies cap at eight. Extra slots
retain bounded protocol recovery. Middleware shares decisions for alternate
entry points such as images. Downstream cancellation stops further attempts.

## Failure classification

Only raw executor responses before BPS translation are trusted. Local Key or
translated errors never qualify as upstream switch evidence.

| Failure | Action |
| --- | --- |
| Exact message `403: This request was blocked by our usage policy.`, `type=server_error`, `code=null` | Request-local switch only; no permanent capability inference |
| `basispoints_model_access_changed` / `model_access_denied` | Same-account fallback may qualify; unsupported only for this path/exact model |
| `codex_access_restricted` / `upstream_access_denied` | Same-account fallback may qualify; path/model 30-second cooldown |
| Explicit structured safety/cyber rejection | No cross-path switch, including subsequent attempts |
| Local permission, quota or group rejection | No switch |
| Unknown 403, unrelated text, HTML/WAF | No switch; unknown JSON 403 remains request-scoped rather than a new account ban |
| Authentication, billing, ordinary rate limits or generic 5xx | Existing shared credential/quota/retry handling; no permanent per-path inference |
| Protocol / encrypted-content errors | Existing dedicated recovery, shared attempt budget |

The exact ambiguous shape requires raw HTTP 403 or HTTP 200 SSE/WebSocket
`response.failed`/`error`. For stream failure, actual HTTP remains 200 and reported
status is 403. Empty/absent/non-null codes do not match. This recognizes a shape,
not its underlying business cause. It never establishes content safety or a
permanent account restriction from free text.

Output or reported token usage on a failed attempt blocks internal switching,
so existing accounting retains that failure. Zero-usage hidden attempts have
separate diagnostics; final client accounting and the single account lease
remain in existing handlers. Native effort accounting follows the actual path.
New diagnostic logs exclude prompts, encrypted contents and credentials.

## State, history and streaming

Native uses its native executor, identity headers, image tools and web-search
format. Each native attempt clears the old State bypass and checks exact
account/model State. It never receives a BPS tool envelope or routing fields.
The effective model is unchanged except by existing explicit mapping.

The inspector buffers empty created/in-progress metadata only: at most 16
events and less than 128 KiB (64 KiB plus one line of lookahead). Text, tools,
unknown events, terminal events or the size limit commit the path. No cross-path
replay follows. On switch, discard the first attempt's metadata, yielding one
event sequence. Transport errors, including WS close codes, survive inspection;
normal long responses stream without full-response buffering.

Complete plaintext function/custom-tool call-and-result pairs are retained.
Unpaired tools, `previous_response_id`, encrypted reasoning, references and
compact items are rejected at a proposed switch with `codex_route_history_required`
or `codex_route_history_incompatible`. Supply complete normalized plaintext
history to recover; ciphertext is never reinterpreted and tool results are not
fabricated. Existing bounded owner-isolated caches can normalize history before
this boundary; no cross-Key/session sharing is added.

Compact supports pre-commit fallback to native JSON, but old compact items are
not portable between paths. BPS is HTTP. With the global BPS setting enabled,
the existing downstream WS handler also downgrades native attempts to HTTP,
even for native-only Keys; it does not claim native upstream WS parity.

## Persistence, recovery and administration

Startup idempotently adds `account_codex_paths`, `account_codex_capabilities`
and `account_codex_probes`, with the capability index and credential generation
column. Old accounts default to allowed/unknown and old Key JSON to
inherit. Credentials, groups and existing account data are untouched.

Terminal success, not HTTP 200, establishes support. Timestamp CAS prevents old
failures overwriting newer successes. Reset fences also cover previously unseen
models. PostgreSQL row-locks the account for observation/reset serialization;
SQLite uses existing write transactions. Admin permission always overrides
success. Control-plane updates reload the local account and invalidate existing
Key caches; other processes refresh persisted path state within five seconds.

Cooldown is in-memory and expiring. After expiry one process-local request per
account/path/model probes; inconclusive probes wait another 30 seconds. Gates
are not distributed across replicas. No automatic paid pool sweep is added.
Reset clears evidence and health without enabling a disabled path or making
paid calls. An explicit administrator BPS capability probe can establish initial
evidence without relaxing a business Key's supported-only filter. Ordinary
connection tests follow the configured route policy and can succeed through
native Codex; their overall success alone does not establish BPS support.

- `GET /api/admin/accounts/:id/codex-routes?model=<exact-model>` returns separate
  permission, capability, source/reason/time and health.
- `POST /api/admin/accounts/codex/routes` takes `ids` (1–100), `upstream` and
  `allowed` and/or `reset_observations:true`. Validate the full batch first;
  database failures may still cause partial success across accounts: refresh.
- Account server-side selectors accept `capability_model` and `capability`:
  `bps_supported`, `bps_unsupported`, `bps_unknown`, `dual_supported`,
  `codex_only_supported`, `bps_only_supported`, `cooldown`, `admin_disabled`.
  Exact model is required. Only-one-supported means the other is explicitly
  unsupported, not unknown. Filtering/totals precede pagination. State stays
  independent. Empty authorized intersections return explicit bounded errors.
- Key editing reuses groups and adds route/filter fields. Account lists show
  both paths with evidence tooltips, detail and batch controls; EN/ZH/ZH-TW.

## BPS strong tests

In Accounts, open the Codex route controls for one account or a selected batch,
enter the exact model, and choose **Basic** or **Tools** under **BPS Strong Test**.
These are explicit administrator requests that may consume upstream quota.
Basic sends one request; Tools sends up to three (basic, echo tool call, tool
result). The echo runs locally and has no external side effect. No automatic
account sweep is enabled.

The probe pins the selected OAuth account, BPS path and exact model. It never
uses native fallback, account rotation or model substitution. It may retest
unknown/unsupported evidence and bypass a business Key's supported-only filter,
but must pass the global BPS switch, model allowlist, account/path administration,
current account and path health, and configured proxy availability. Native turn
State is unnecessary for this BPS request. Before each subsequent tool step,
the probe reloads path configuration and rechecks health and credentials.

HTTP 200 alone cannot pass. Basic requires a valid successful `response.completed`
and the generated nonce as text. Tools additionally requires exactly one
correct nonce echo call with `encrypted_function_args: []`, then a completed
response after returning the tool result. A failed/incomplete terminal event,
missing terminal, wrong nonce or malformed tool declaration fails its stage.
This validates the server's BPS tool roundtrip; it does not certify a particular
client's multi-agent scheduling or end-to-end subagent behavior.

The result reports the actual path/model, overall outcome, independent basic
and tools outcomes, current evidence, actual HTTP status, separately reported
upstream status, safe error code, times and dispatched request count. For example,
an HTTP 200 stream reporting a 403 is blocked, not successful. A tool protocol
failure can retain genuine basic support. A later explicit raw model denial
overrides that support for the exact model. Ambiguous 403, authentication,
workspace, rate-limit and network failures never establish permanent unsupported
evidence and never mark the entire account bad from this test.

- `POST /api/admin/accounts/codex/probe` takes
  `{"ids":[123],"model":"gpt-6-astra","level":"tools"}` and returns
  `{results,total,completed}`. Use `?stream=true` for SSE `start`, `testing`,
  `result`, and `done` messages. An authenticated administrator is required.
- Maximum 100 selected accounts, three concurrent probes per server handler,
  and one active probe per account. Overlapping account probes are skipped.
  Canceling the browser request cancels active calls and prevents new dispatches;
  already received results remain visible. These concurrency guards are local
  to each server process.
- `GET /api/admin/accounts/:id/codex-probes?model=<exact-model>` returns saved
  results. The existing `codex-routes` response also includes `probes`.
- Additive `account_codex_probes` storage keeps the latest result per
  account/path/exact-model/level, not an unbounded run log. Results survive restart.
  Results and capability evidence use credential and timestamp fences; tests
  started before a reset or credential replacement cannot publish new evidence.
  Successful OAuth rotation carries completed evidence forward only when the
  same user and workspace identity are confirmed; the credential fence still
  advances and rejects in-flight results from the old token. Administrative
  credential replacement or uncertain/changed identity requires new evidence.
  Historical results describe their recorded test time, not present availability.
  Business supported-only filtering still uses basic path/model capability;
  passing the stronger tools level is displayed separately.

## Example A: BPS-only authorized accounts

Partial Key payload (group IDs are examples of existing Codex groups):

```json
{"allowed_group_ids":[10],"limits":{"upstream_channel":"codex","codex_route_policy":"basispoints_only","codex_capability_filter":"basispoints_supported"}}
```

Enable global BPS and allowlist the effective model. Only group 10 accounts with
confirmed exact-model BPS support qualify; no native fallback is allowed.

## Example B: Dual support, BPS preferred

```json
{"allowed_group_ids":[20],"limits":{"upstream_channel":"codex","codex_route_policy":"basispoints_prefer","codex_capability_filter":"dual_supported"}}
```

Enable BPS and `BASISPOINTS_NATIVE_FALLBACK=true`. Both paths need exact-model
success evidence. An ambiguous BPS rejection can switch once on the same account
if native State is valid. A new explicit BPS model denial invalidates the strict
dual-support filter; it cannot be bypassed for fallback. Use `any` only when
that broader capability criterion is intended, with the same group boundary.

## Deployment and rollback

1. Back up database, environment and settings. Deploy only with separate
   operational authorization; no deployment is performed by this change.
2. Start the new binary; additive schema initialization is automatic. Canary
   restricted Keys/groups and explicit models before expanding traffic.
3. Verify only/prefer policies, ambiguous pre-output failures, State refusal,
   cancellation, one event sequence, final usage and one lease with mocks.
4. Verify reset/disable, pagination, NoAffinity behavior and multi-process refresh.
5. Roll back by draining requests and restoring the prior binary. It ignores
   additive tables/unknown Key fields, including NEW RESTRICTIONS: first disable
   affected Keys or restore equivalently restrictive old groups/configuration.
   Keep additive tables for rolling forward; never drop them under new workers.

Validation commands: `go test ./...`, targeted `go test -race` on auth/database/
admin/proxy, `go build -o <temporary executable> .`, frontend typecheck/test/build,
and `node frontend/tests/codex-routes.browser.test.mjs` with a Vite server.
Optional PostgreSQL integration needs `CODEX2API_TEST_POSTGRES_DSN` pointing to a
disposable test DB. Browser fixtures are entirely synthetic; optional
`CODEX_ROUTES_BROWSER_PATH` selects the browser. No production/paid probes.
