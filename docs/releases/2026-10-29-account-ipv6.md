# 2026-10-29 Codex2API account IPv6 deployment

Target: netcup `159.195.12.230`, Codex2API only. User authorized implementation,
merge, push and downtime deployment, plus `.worktrees/codex-reported-client/`.
Production is healthy and the verified 128-address pool is enabled. Existing
seven accounts remain disabled and were not bulk-enrolled.

## Delivered behavior

- Local IPv6 membership persists independently of `proxy_url`; account main and
  quick editors show the actual binding/pending state and permit manual selection
  of another free pool address. Atomic expected-current checks reject stale,
  occupied or cooling selections. Old connection leases prevent address reuse
  until actual connection close, followed by cooldown.
- Real proxy or explicit blank exits membership; blank uses host IPv4. New native
  accounts default to membership if no explicit source preference exists.
  Allocation remains lazy and existing automatic rotation is retained.
- Imports default to `America/New_York` with a checked control; unchecking sends
  an explicit empty default. Explicit source timezone is used for new accounts;
  duplicate accounts preserve existing timezone. Explicit source blank proxy
  blocks form fallback when source proxy import is enabled.
- `reported-client` was ported from the mixed historical source snapshot, rather
  than copying its old tree: pool persona kind follows recognized official daily
  client usage; unknown/missing/error history retains weighted fallback. Current
  version validation, transport and IPv6 routing remain in place.
- Dedicated ipvlan attachment is restored by a systemd watcher and release
  readiness hook. Only Codex2API's network namespace is changed.

## Provenance

Application built from clean pushed root `main`, verified against freshly fetched
`origin/main` by commit and tree, non-detached and clean:

- Application commit: `f8721a4a410413d7d798afd960ba6278c114c75a`
- Application tree: `ca4edd150dc4800708be4cf11bb261dda635299b`
- Upstream: `d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1`
- Build version: `release-20261029-f8721a4a4104`
- OCI index: `sha256:13af9f3b82861ab6ac01187bc0c7113d06dd17a3114a23619c426060c443ce5f`
- Loaded amd64 image/config: `sha256:388ba8077ca684e346dfaf53fd2ec1156eb1d96bdd5c1f095efe99effc4589e7`
- Binary SHA256: `4cc26143e85c376e67d8d4bc4b0486ea891090b530188fe08143fbc8bd4a830f`

The network helper received a direct file update from separately clean pushed
`main` during recovery; application image and binary did not change:

- Helper commit: `efbbe65f70847dccf2c642511f6aafa7e145ee82`
- Helper tree: `89b7a8d00a1773cbd11de53904cfaa69cd6a8500`
- Installed helper SHA256: `81432ccaae690756cc459d978c697eb2d2cc8422b3c0ea301d96a3f29c605971`
- Original staged helper retained as `ipv6_pool.original.py`; direct update and
  source distinction recorded in protected `network-recovery.json`.

Integration branch `codex/account-ipv6-20261029` and its backend/frontend/reported
client candidates are merged historical evidence. Do not redeploy old candidates.
The final documentation/regression-test commit adds no deployed application code.
The original dirty shared checkout and mixed source snapshot remain preserved.

## Validation and production evidence

Reused full Go suite (all packages passed), affected package/race tests,
reported-client/UA tests, frontend build, and existing release checks. Additional
manual-selection tests cover concurrency, old HTTP/WS leases, cooldown extension,
source allowlist, disabled membership/pool, and unchanged account enablement.
Final frontend typecheck and 25 Chromium browser cases passed. Final release
suite: 102 tests passed. A full mocked fresh-interface `ensure` regression
reproduces the former IndexError against the old helper and passes after repair.

Live application tests used disabled account 85 with `codex-auto-review`, prompt
“Reply with exactly OK.” Both returned successful completion and `OK`; observed
IPv6 socket sources matched the account's actual binding:

| Action | IPv6 | Inference duration |
|---|---|---|
| Automatic claim | `2a0a:4cc0:101:799:c2a:28:0:3` | 2.636 s |
| Manual switch | `2a0a:4cc0:101:799:c2a:28:0:4` | 13.205 s |

Stale expected-IP and cooling-target writes returned 409. The test account's
original absent membership field, empty proxy and disabled state were restored.
The test endpoint pools WebSockets; only this disabled account's idle source
sockets were closed after active-request count reached zero, exercising actual
connection-close lease release. Binding removal and both cooldowns were verified;
final active lease count was zero. No production lease table was forcibly cleared.

Final public health/version and binary match passed. All 25 protected containers'
IDs, StartedAt and configuration fingerprints were unchanged. Shared Caddy routes
match the original configuration. Intelligence settings, enable flags, history,
account enablement and credential-worker configuration/heartbeat were retained.
Test station was not queried or synchronized; cold standby was not accessed.

## Timings, failure and recovery

All times below are Asia/Shanghai on 2026-10-29:

- Final artifact build: 18.478 s; completed 02:42:32.
- Identity verified 02:44:15; rollback image verified 02:44:18; isolated restored
  database migration rehearsal passed 02:44:26 (about 11 s combined preflight).
- Maintenance 02:44:27.760 → traffic restored 02:55:05.720: **637.960 s**
  (10 min 38 s). Public verification/source recording completed 02:55:30.
- A preliminary attempt rejected a same-gateway RA route before maintenance and
  restored the attempted unit installation; no application outage then.
- The main attempt timed out readiness because a fresh interface's
  `ip -j -6 addr` inventory was empty. The controller stopped the candidate and
  preserved the current database/maintenance after the writer-start boundary.
  The helper was fixed, verified, committed/pushed, installed atomically and the
  same candidate restarted. Readiness and full smoke then passed before opening
  traffic. No old database snapshot was restored, and no old application rollback
  was performed. The maintenance window exceeded the initial estimate.

## Configuration and recovery entry points

- UI: **Proxies → 本机 IPv6 出口池**; database `ipv6_egress_config`.
  Enabled, cooldown 600 s, max attempts 3, retry 5xx enabled, explicit 128-address
  allowlist. Resin remains disabled.
- Network config: `/opt/codex2api/ipv6-pool.json`; helper
  `/opt/codex2api/ipv6_pool.py`; systemd `codex2api-ipv6-pool.service` active/enabled.
- Release: `/opt/codex2api/releases/account-ipv6-20261029-f8721a4a/`.
- Protected recovery/evidence:
  `/opt/codex2api/backups/account-ipv6-20261029-f8721a4a/` contains exact prior
  compose/route/source/binary, rehearsal/stopped dumps (stopped dump 9,837,047
  bytes), final verification and acceptance records.
- Exact prior-binary rollback image:
  `sha256:de940e12184a909c7e24b18ef5622d178be54ac10458d9a993b1122ed406a28a`.
  Before an old-version rollback disable the global IPv6 pool; preserve the latest
  database and verify backward compatibility. Do not blindly restore the stopped
  dump after new writes. Use the common `/var/lock/codex2api-release.lock`.

Known limitation: crash-orphan durable leases have no automatic expiry. After a
forced kill or failed lease release they can reserve capacity until verified
stopped-instance recovery. Ordinary stream close/shutdown releases leases;
capacity exhaustion fails closed. Prepared recovery SQL is protected outside Git
and was not executed. See `docs/ipv6-egress.md` for prerequisites.
