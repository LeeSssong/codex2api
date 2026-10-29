# Current production: account-bound egress with direct fallback — deployed 2026-10-29

User authorized implementation on 2026-10-29, initially deferred deployment, then
explicitly authorized immediate production deployment on 2026-10-29.
Corrected failure policy: if a bound account proxy, fixed route, or native IPv6
route is unavailable, use host direct only; never substitute global/pool/Resin
or another account route. Preserve one-shot request semantics: no replay after
request headers/body may have been sent or after a stream begins.

Owner/worktree: current Codex chat at
`/Users/gongtengxinwen/.codex/worktrees/account-egress-fallback/codex2api`,
branch `codex/account-egress-fallback`, base `7b28052976f9a1dc5f00fc1da3f77416d093bf55`.
Fix commit `8fb0a05164450a892fba16c019f1dd728f7ca9ee` was merged into clean main
and deployed as source `0671dbb7fda8cc5555f45233388604f9914133b2`, tree
`4d0f8b9aaf926a87072fbc34664ec904a1a71ff4`, upstream
`d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1`. Target image/config digest
`sha256:34dc81bba1bcec6e0eac6e6dda56604ac1c72c10e035b2e6777cdd0d5dc29875`,
binary SHA256 `881703bf7517b7072030c46c5656105021ff58a8502be786502a60f57b56529e`,
container `98ea0eaa70f32dd7500ae333f1e227a4a7da6c7f4e852f29576e655812af471f`,
version `release-20261029-0671dbb7fda8`. Release, maintenance and recovery
details are in `docs/releases/2026-10-29-account-egress-fallback.md`.

The new account-bound egress policy is live. The user's clarified failure policy
is direct-host fallback only when a bound proxy/fixed/native-IPv6 route is
unavailable; never substitute global proxy, pool, Resin or another account's
egress. Do not replay after HTTP request bytes may have been sent or streams
start; WebSocket fallback is before handshake only.

Public health and authenticated admin smoke passed; uncredentialed `/v1/models`
returned expected 401. Health reports `available=0,total=7`; all seven active
inference accounts remain disabled, so a real upstream inference smoke was not
run and this deployment does not itself make the service serve inference.
Intelligence detection remains `enabled=false`, interval 900 s, recovery 3600 s,
revision 16; retained history `[265,265]`. Credential worker is online. All 25
non-target containers/configurations and the non-Codex Caddy routes are unchanged.

Verification: clean-main serial full Go suite `go test ./... -p 1 -count=1
-timeout=8m` passed every package; focused race
`go test -race ./internal/egressroute ./proxy/wsrelay -count=2`, `go build ./...`,
frontend build and `git diff HEAD^1 HEAD --check` passed. Release controller tests
passed 39 checks when run from `deploy/release`.
Parallel full-suite execution once encountered an unrelated credentialops
child-process environment flake; the serial suite and five isolated repetitions
of that test passed. Details and remaining rollout caveats are in
`docs/superpowers/plans/2026-10-29-account-egress-fallback.md`.

The commit-added task plan remains historical implementation context; this
release supersedes its earlier local-only status. Do not redeploy a stale
candidate, restore the pre-release DB dump after new writes, enable accounts, or
alter unrelated services.

---

# Previous production: 2026-10-29 account IPv6 + reported client

Deployed Codex2API application `f8721a4a410413d7d798afd960ba6278c114c75a`,
tree `ca4edd150dc4800708be4cf11bb261dda635299b`, upstream `d967cda1`.
Image/config `sha256:388ba8077ca684e346dfaf53fd2ec1156eb1d96bdd5c1f095efe99effc4589e7`.
Persistent IPv6 helper directly updated from clean pushed main `efbbe65f`, hash
`81432ccaae690756cc459d978c697eb2d2cc8422b3c0ea301d96a3f29c605971`.
128-address pool enabled, 600 s cooldown/3 attempts/5xx rotation. New native account
membership and New York import defaults, actual IP display, manual pool selection,
source preferences and duplicate timezone preservation shipped. Historical
reported-client snapshot feature port included. Allocation remains lazy.

Live automatic and manually selected IPv6 inference both returned OK; original
seven disabled account states preserved. Test account preferences restored and
binding/connection leases released. All 25 protected containers and original
shared ingress verified unchanged. Credential worker and intelligence settings,
flags/history preserved. No Sub2API/database/Redis restart or cold-standby action;
test station not queried/synchronized.

Maintenance 02:44:27–02:55:05 Asia/Shanghai, 637.960 s. Fresh-interface network
helper recovery used the same application image and current database, no old dump
restore. Final release suite 102 tests and browser suite 25 tests passed. Known
crash-orphan lease recovery limitation documented. All task candidates are merged
historical evidence; original dirty checkout/source snapshot remain preserved.

One release record/recovery entry: `docs/releases/2026-10-29-account-ipv6.md`.
Earlier handoffs below are historical; do not redeploy stale candidates.

---

# Current status: relogin + intelligence recovery interval deployed

User authorized downtime deployment on2026-10-28. Both tasks merged/pushed to main;
production source `884392a122b3abcaab74b1cd20cd05111aab06a3`, tree `6f8aafa7f9fab4a1664de856502d2882f8e60a0d`,
actual binary `bba286e114ece31d2a4f5d4517018c06572456ca1e31508119f3996cd8a7e4a9`,
image `sha256:38e6e7997290eeb5642fe712751a54091e685a7e4a1ae800ee04785f764bfcd5`. Later documentation commits do not require redeploy.
Maintenance 2026-10-28T02:22:31.216+08:00 to 2026-10-28T02:22:44.124+08:00
(12.907 seconds); independent final verification passed. Exactly one writer,
worker online, maintenance removed, unrelated containers unchanged.
Current settings preserved: enabled=false, ordinary interval600, revision13;
new recovery interval3600 returned without changing saved intent. History [104, 104].
No live login probe, Sub2API/database/Redis restart or cold-standby action.

Evidence/recovery: docs/releases/2026-10-28-relogin-recheck.md. Current production
has new writes; never restore older snapshots or redeploy stale candidates.
Interval source5aaf8ad5 and credential355d46e4 merged via e0480e24; all10 interval
snapshot hashes match; shared dirty original remains preserved. Release clone
main is independent of shared original; no forced main update.

Earlier handoffs follow as history; pending/local-only statuses are superseded.

---

# Active release: relogin state + intelligence recovery interval

2026-10-28 user explicitly authorized merging `codex/intelligence-recheck-interval`
and downtime deployment to netcup159.195.12.230. This supersedes the earlier
local-only instruction for this release.

Interval worktree's10 dirty files were snapshotted with hashes outside Git,
committed `5aaf8ad5`, merged via `e0480e24` with credential head `355d46e4`.
Upstream `20594822` is included. Shared dirty original remains untouched.
Merged full Go/race tests,458 frontend units, typecheck/build,9 browser tests,
85 release checks and isolated LOCAL Redis/PostgreSQL integration passed.

Production baseline e61b04c3, live detection revision13/enabled=false/interval600;
preserve current switches/settings/history. New recovery interval defaults3600
at read time. Build clean pushed main in an independent clone. Deploy only via
common lock and maintenance_release.py with exact identity checks, restored DB
rehearsal, stopped backup and single writer. Result:
`docs/releases/2026-10-28-relogin-recheck.md`.

Earlier handoffs below are historical and their no-deploy limit is superseded.

---

# Current task: 2026-10-28 credential relogin state — local only

Latest user instruction: implement the diagnosed fix, **do not deploy yet**.
Owner/worktree: current Codex chat,
`/Users/gongtengxinwen/.codex/worktrees/credential-relogin-state-audit/codex2api`;
branch `codex/credential-relogin-state-audit`. Original base `69abb079`;
diagnosis `6aed5588`, implementation `55dfa24a`, merged upstream `20594822`
via `611c69fd`. Final reviewed candidate is the branch head after `611c69fd`; nothing pushed,
merged into main or deployed by this task.

The fix reconciles committed Codex credential generation, shared unauthorized
cooldown and runtime state on completion/replay/retry. Conditional SQL writes,
generation-aware atomic cache cleanup and retired-object scheduler guards keep
late old requests from re-banning the replacement. Quota/manual pause/monitor
settings and independent Excel grant state remain intact. No frontend source
changes or schema migration. Local implementation is complete: full Go suite (24 packages), three race
repetitions, Go/frontend builds and diff validation passed. Evidence is in
`docs/investigations/2026-10-28-credential-relogin-fix-plan.md`.

Operational caveat: optional Redis integration accidentally used the existing
remote Docker context `sub2api-prod-build`. Only isolated temporary no-volume
`codex2api-relogin-test-redis` was started, then immediately removed; exact-name
listing confirmed absence and user was informed. No application deployment,
production account/data change, real login or production service restart.
Real Redis/PostgreSQL integration is not verified. No further remote operations.

The following production handoff is **preserved upstream history**, not a
release or live-state verification performed by this credential-relogin task.
Do not redeploy historical candidates or infer deployment authorization.

---

# Preserved upstream handoff: 2026-10-28 intelligence cooldown fix — deployed

Production Codex2API on netcup 159.195.12.230 runs source
`e61b04c3b9a72cbd75066ec09f7f5be7bf7f75cb`, tree
`23d4b944ea0193fb07bc4b451ee5901954cf4e77`; authoritative record is
`/opt/codex2api/source-release.json`. Documentation commits after this source do
not require another deployment. Upstream `3c6adfda702efed67931b2d98a491d72bd9d91b5`
was fetched again after deployment and is already an ancestor.

Automatic checks defer during cooldown/quota exhaustion and resume when available.
Enqueue, claim, runtime and final HTTP/WebSocket dispatch enforce availability;
manual switches, frequency, enrollment and history remain intact. Manual probes
and intelligence-owned quality recovery retain their prior behavior.

The user ultimately requested direct downtime deployment. Initial maintenance was
2026-10-28 01:12:04.386–01:12:20.877 +08:00 (16.491 seconds). External concurrent
Sub2API deployment replaced three non-target containers during public smoke,
triggering protective maintenance at approximately 01:14:19.858. Recovery retained
the current database and current non-target routing, restarted the same candidate,
and reopened traffic at 01:17:24.560 (second window about 184.702 seconds).
Total maintenance about 201.192 seconds. No old dump was restored. Recovery public
smoke passed at 01:19:06; independent final verification passed at 01:22:05.

Detection: enabled=true, interval600, revision12; history103 retained. Account
switches and enrollment are verified by before/after fingerprints. Credential
worker is online, exactly one Codex2API application is running, and maintenance
route/network gate are removed. Only Codex2API was stopped/started by this task.

Release and recovery details: docs/releases/2026-10-28-intelligence-cooldown.md.
Owned worktree `/Users/gongtengxinwen/.codex/worktrees/intelligence-quota-cooldown/codex2api`,
branch `codex/intelligence-quota-cooldown`. Shared original directory remains
preserved; all 52 recorded file hashes match. Existing snapshots/worktrees are not
deleted. Do not redeploy historical candidates or restore pre-release databases.


---

# Current task: 2026-10-28 web_search / Responses Lite WebSocket isolation — local only

User authorized implementation and tests but explicitly said **do not deploy yet**.

- Owner/worktree: current chat; `/Users/gongtengxinwen/.codex/worktrees/websearch-ws-mode-isolation/codex2api`
- Branch/base: `codex/websearch-ws-mode-isolation` from `d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1`; current origin/main stayed at that SHA.
- Diagnosis: account 85 is OAuth Plus/active; ordinary HTTP and fresh ordinary WS web_search succeeded, while Lite→ordinary reuse on one upstream WS reproduced the exact `rustponsesapi` 400.
- Implementation: effective final WS Lite marker is carried only in local context; transport pool keys and immutable dial/continuation identities distinguish Lite from standard. Stateless reusable pools, explicit sessions, fallback lanes, busy overflow, send-redial and previous_response_id matching are covered. No upstream headers, prompt_cache_key, thread/session IDs or chat history are changed.
- Local files: `proxy/wsrelay/responses_lite_mode.go`, `proxy/wsrelay/responses_lite_mode_test.go`, `proxy/wsrelay/executor.go`, `proxy/wsrelay/connection_dial.go`, `proxy/executor.go`; plan `docs/superpowers/plans/2026-10-28-websearch-ws-mode-isolation.md`.
- Verification: baseline wsrelay/proxy passed; mode regression passed; focused race run (`go test -race -p 2 ./proxy/wsrelay -run '^TestResponsesLiteMode|^TestContinuation|^TestWebsocketContinuation|^TestIPv6Websocket' -count=3`) passed; `go build ./...`, frontend `npm ci && npm run build`, `git diff --check` passed; isolated `go test ./credentialops -count=1` passed. First full `go test ./...` had one unrelated timing failure in `credentialops/TestCredentialOperationsRuntimeMakesRelativeStatePrivateAndJournalAbsolute`; all other packages including `proxy/wsrelay` and `proxy` passed.
- Status: candidate remains local and unpushed/unmerged/**NOT DEPLOYED**. Production container/settings/database were not touched.

## 2026-10-28 web_search WS isolation — deployment authorized

- User now explicitly authorizes production deployment, superseding earlier implement-only/no-deploy restriction. Owner: this chat; isolated branch codex/websearch-ws-mode-isolation; candidate e0ecb08c; base d967cda1.
- Target netcup 159.195.12.230, Codex2API only. Preserve settings, credentials/runtime, database and Redis; use verified pushed main and common locked maintenance controller, exact live baseline and rollback artifacts. Release verification and deployment are in progress; earlier NOT DEPLOYED entries are historical implementation status, not a new deployment result.

## 2026-10-28 web_search WS isolation — deployed

- Latest user authorized deployment; source 2425875612935cc5244a7d844f27d09406e7bee8, tree fb12a3c54786eb9cb4547d56b8b0502c27464bc2, binary 2d7de25e4e296fa873b0849de92dd982acd4ab80bd6824cfe991f16938e815cc, image sha256:a94de947602e1d401bee620bf0ef146b597b113b6a79a6ec98fb7069aa0ac7e9. Fix e0ecb08c merged/pushed and deployed from clean current main. Earlier local-only status is historical.
- Maintenance 14.907s. Full Go suite, targeted race x3,85 release tests; internal/public smoke and independent final checks passed. One writer, credential worker online; exact settings/accounts/runtime retained; non-target services unchanged.
- Actual intelligence revision15/enabled=true/interval900/recovery3600, history[242,242]. All7 active accounts remain disabled by prior configuration: no real-account search smoke and no auto-enabling.
- Release/recovery: docs/releases/2026-10-28-websearch-ws-mode-isolation.md; protected evidence /Users/gongtengxinwen/.config/codex2api/websearch-release-20261028/; artifacts /opt/codex2api/releases/websearch-ws-20261028-24258756/, backups /opt/codex2api/backups/websearch-ws-20261028-24258756/. New writes possible: never restore old dump or rerun stale release. Original shared dirty checkout preserved.
