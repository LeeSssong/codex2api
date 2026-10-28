# 2026-10-28 web_search / Responses Lite WebSocket isolation release

## Authorization and source

User explicitly authorizes deployment on 2026-10-28; earlier local-only restriction is superseded. Target netcup 159.195.12.230, Codex2API only. No account/entitlement/settings/schema change. Candidate fix e0ecb08c is based on d967cda1 and separates standard and Lite upstream sockets, including continuations, pool fallback and retries. Do not disable web search or reauthenticate accounts.

## Fresh verification

`GOMAXPROCS=2 go test -p 2 ./... -count=1` passed, including credentialops. Focused wsrelay race tests passed three repetitions. All release-tool unittest checks passed. `git diff --check` passed. Build will use an independent clean main clone and explicit local Docker context colima-build, never the implicit remote context.

## Release preparation

Revalidated live source 884392a122b3abcaab74b1cd20cd05111aab06a3, executable bba286e114ece31d2a4f5d4517018c06572456ca1e31508119f3996cd8a7e4a9, image sha256:38e6e7997290eeb5642fe712751a54091e685a7e4a1ae800ee04785f764bfcd5, container eff66a0348d6d1cecd53786c7ad4f955769bae6c3d434d5865d2bad721162a2b, Compose /opt/codex2api/compose.netcup.json. Active requests were zero; app container remains running. Preserve original routing, credential-runtime mount and saved monitor configuration.

Use tested maintenance_release.py with common /var/lock/codex2api-release.lock, baseline guards, exact-executable rollback image, restored disposable database rehearsal and validated stopped backup. No PostgreSQL/Redis/Sub2API/cold-standby restart. After new writes, never restore an old dump. Protected evidence: /Users/gongtengxinwen/.config/codex2api/websearch-release-20261028/.

Status: authorized, verified candidate; deployment pending. Actual final identity, downtime and verification will be appended after release.
