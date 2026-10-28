# 2026-10-28 web_search / Responses Lite WebSocket isolation release

## Authorization and source

User explicitly authorizes deployment on 2026-10-28; earlier local-only restriction is superseded. Target netcup 159.195.12.230, Codex2API only. No account/entitlement/settings/schema change. Candidate fix e0ecb08c is based on d967cda1 and separates standard and Lite upstream sockets, including continuations, pool fallback and retries. Do not disable web search or reauthenticate accounts.

## Fresh verification

`GOMAXPROCS=2 go test -p 2 ./... -count=1` passed, including credentialops. Focused wsrelay race tests passed three repetitions. All release-tool unittest checks passed. `git diff --check` passed. Build will use an independent clean main clone and explicit local Docker context colima-build, never the implicit remote context.

## Release preparation

Revalidated live source 884392a122b3abcaab74b1cd20cd05111aab06a3, executable bba286e114ece31d2a4f5d4517018c06572456ca1e31508119f3996cd8a7e4a9, image sha256:38e6e7997290eeb5642fe712751a54091e685a7e4a1ae800ee04785f764bfcd5, container eff66a0348d6d1cecd53786c7ad4f955769bae6c3d434d5865d2bad721162a2b, Compose /opt/codex2api/compose.netcup.json. Active requests were zero; app container remains running. Preserve original routing, credential-runtime mount and saved monitor configuration.

Use tested maintenance_release.py with common /var/lock/codex2api-release.lock, baseline guards, exact-executable rollback image, restored disposable database rehearsal and validated stopped backup. No PostgreSQL/Redis/Sub2API/cold-standby restart. After new writes, never restore an old dump. Protected evidence: /Users/gongtengxinwen/.config/codex2api/websearch-release-20261028/.

Status: DEPLOYED and independently verified; real-account search acceptance remains unperformed because all accounts are disabled.

## Deployed and independently verified

- User-authorized release completed on 2026-10-28. Source `2425875612935cc5244a7d844f27d09406e7bee8`; tree `fb12a3c54786eb9cb4547d56b8b0502c27464bc2`; real-time origin/main matched immediately before dispatch. Fix commit e0ecb08c is included. Source and previous production are proven ancestors. Later documentation commits need no redeployment.
- Executing binary SHA256 `2d7de25e4e296fa873b0849de92dd982acd4ab80bd6824cfe991f16938e815cc`; server image/config digest `sha256:a94de947602e1d401bee620bf0ef146b597b113b6a79a6ec98fb7069aa0ac7e9`; container `ccdaf34feb6232194545c3a556b3d0cace890858f448c8d4bde416850580601f`. Version `release-20261028-242587561293`.
- Maintenance started 2026-10-28T22:33:12.673+08:00, traffic route restored 2026-10-28T22:33:27.579+08:00; actual recorded maintenance **14.907 seconds**. This restores the prior route, not an instruction to enable disabled accounts.
- Internal/public health, settings, quota/account stats, intelligence history and admin UI smoke passed. Independent verification at 2026-10-28T14:36:45.308350+00:00: one writer, fresh embedded credential-worker heartbeat, exact retained configuration and runtime mounts, unchanged protected containers, maintenance/network gate removed.
- Actual saved intelligence settings revision15, enabled=true, interval900 seconds, recovery3600 seconds; history [242,242]. These supersede historical snapshots from the prior release and were preserved exactly. All seven nondeleted accounts 78,79,81,82,83,84,85 are enabled=false. No account was enabled, unlocked, reauthenticated or otherwise changed by this release.
- **Real upstream web_search smoke not run**: all accounts are intentionally disabled, consistent with the user's no-service statement. Do not silently enable an account for a test. Automated exact-error reproducer, both mode orders, pool/fallback/retry and continuation safety plus repeated race regression pass. Runtime binary identity proves this fix is installed, but does not substitute for a future real-account acceptance test.

## Build deviations and evidence

Repository guard refused a feature-branch push to main; no guard was bypassed. A separate clean main checkout integrated the exact candidate and pushed normally. Slow full-network clone was replaced by a separate local shared-object clone with remote origin restored and freshly fetched. Original dirty shared checkout was never modified.

release_build.py built frontend and Linux/amd64 binary from clean pushed main. Docker Hub metadata/auth resolution timed out; no production changes had occurred. Packaging reused the exact verified previous runtime image (including matching source labels and previous binary hash), replaced only the application binary, set new provenance labels, used explicit LOCAL colima-build and no-network build/hash-only containers. New image binary hash verified before upload. Local OCI index `sha256:f272a47b114fb7d3845b76ae991366bfe51126ae7b43494f4abe95e589d4dd81` differs from classic Docker's server config digest above; maintenance verifier validated actual server image labels/binary. Full evidence remains in `/Users/gongtengxinwen/.config/codex2api/websearch-release-20261028/`.

## Backups and recovery

- Release artifacts: `/opt/codex2api/releases/websearch-ws-20261028-24258756/`.
- Preflight/stopped dump, baseline/config/route fingerprints and final evidence: `/opt/codex2api/backups/websearch-ws-20261028-24258756/`.
- Validated stopped backup SHA256 `0abe2d3e984203d723f658ea121f335dfa550ec62ed0ac456f78bebda9ac82cb`.
- Exact previous-executable rollback image `sha256:8b3baa84cd1f469c735392683f0392bdf78b8d7cdc20dff3531e0730064558c2`.
- Restored disposable database migration rehearsal passed before stopping the sole writer; stopped backup validated and retention rechecked. No task schema changes. Database, Redis, Sub2API and other containers were not restarted.

The new app/embedded worker may write even while all inference accounts are disabled. Never restore an old dump blindly. Recovery requires the same common lock and current-identity checks, preservation of newest data, a single stopped writer and forward repair or verified binary/current-schema compatibility. Historical compose.rollback.json is an artifact, not authorization to repeat a release or restore old data.
