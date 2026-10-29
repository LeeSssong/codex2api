# 2026-10-29 account-bound egress fallback deployment

## Scope and behavior

User explicitly authorized immediate deployment on 2026-10-29 to the netcup
Codex2API host `159.195.12.230`; do not deploy to Sub2API, the cold standby, or
the historical host. The account-bound routing fix covers generation, OAuth,
credential workers, auxiliary/maintenance HTTP, native IPv6 and WebSocket
connections. When the bound route is unavailable, it falls back to host direct
only, never a global proxy, proxy pool, Resin or a different account's route.
HTTP requests are not replayed once bytes may have been sent or a stream starts;
WebSocket fallback is limited to the pre-handshake stage. Intelligence history
records the observed route without rewriting older rows.

## Provenance

- Fix commit: `8fb0a05164450a892fba16c019f1dd728f7ca9ee`.
- Deployed source: `0671dbb7fda8cc5555f45233388604f9914133b2`.
- Source tree: `4d0f8b9aaf926a87072fbc34664ec904a1a71ff4`.
- Upstream: `d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1`.
- Build version: `release-20261029-0671dbb7fda8`.
- Docker server image/config ID: `sha256:34dc81bba1bcec6e0eac6e6dda56604ac1c72c10e035b2e6777cdd0d5dc29875`.
- Local OCI index: `sha256:8e87f0953eb8abe32da91a1d0308843fb22bebe3fe8ced6004424a0d3c9e88fe`; target Docker's classic image ID is the config digest above. The loaded image config, source labels, archive checksum and embedded executable were independently checked before maintenance.
- Binary SHA256: `881703bf7517b7072030c46c5656105021ff58a8502be786502a60f57b56529e`.
- Production container: `98ea0eaa70f32dd7500ae333f1e227a4a7da6c7f4e852f29576e655812af471f`.
- Exact previous executable retained as rollback image: `sha256:4676fe575f49b49331daca907cc7f8c5120b4fdb5e848527b6322481b71d0db1`.

Build source was a separate clean `main` checkout. Candidate commit was pushed to
its task branch, merged without conflicts, then main was pushed normally. Exact
`origin/main` SHA and tree were checked before and after build. The repository's
`release_build.py` produced a Linux/amd64 artifact. OCI index and classic Docker
image IDs differ by representation; the manifest records the target config ID,
while preserving both IDs and the image tar checksum in the artifact manifest.
The production release tool validated target image labels and binary identity.
No Docker context targeting the historical host was used.

## Validation and maintenance

- Full serial Go suite, focused race suite, Go build, frontend build, diff checks,
  and 39 release-controller/build tests passed.
- Current live container, source-release record and actual `/proc/1/exe` digest
  matched before release. Exact rollback executable and isolated restored-database
  migration rehearsal passed before maintenance.
- Maintenance started `2026-10-29 22:35:47.010 +08:00`; traffic reopened
  `2026-10-29 22:36:05.531 +08:00`. Recorded window: **18.521 seconds**.
- Stopped single-writer backup: `/opt/codex2api/backups/account-egress-fallback-20261029-0671dbb7/stopped.dump`, 9,845,739 bytes. Preflight and stopped dumps, route/config/container fingerprints, events and command logs are in that protected backup directory.
- Internal version/admin, intelligence settings/accounts/history, quota stats,
  UI shell and credential-worker checks passed. Public health returned
  `status=ok`, version `release-20261029-0671dbb7fda8`; public admin smoke passed.
  Independent unauthenticated `/v1/models` returned the expected 401.
- Intelligence settings stayed disabled at interval 900 s, recovery 3600 s,
  revision 16; history remained `[265,265]`. The worker is online. All 7
  non-deleted inference accounts remain disabled, and health reports
  `available=0,total=7`. No account was enabled. Consequently no real upstream
  inference request was made; the installed code and regression suites establish
  transport behavior, but real bound-route acceptance remains unverified until
  an account is enabled through the user's normal process.
- All 25 protected non-Codex containers and configuration fingerprints, Caddy's
  non-Codex routes, account enablement and retained settings were unchanged.
  Maintenance route and network gate were removed.

## Recovery

Release artifacts are at
`/opt/codex2api/releases/account-egress-fallback-20261029-0671dbb7/`; protected
backup and event evidence are at
`/opt/codex2api/backups/account-egress-fallback-20261029-0671dbb7/`. Any recovery
must use `/var/lock/codex2api-release.lock`, re-check current source/container/
image/binary/Compose, preserve the newest database writes, and operate only on
Codex2API. The new app and embedded worker can write even with all inference
accounts disabled. Never restore `stopped.dump` over newer writes or blindly
restart an older image. Validate old-binary compatibility with the current
database before any rollback.
