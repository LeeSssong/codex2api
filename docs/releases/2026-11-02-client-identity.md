# 2026-11-02 client identity and disabled-account quiet

## Scope

User authorized a direct Codex2API deployment on 2026-11-02 while the site was
not serving external traffic. Target is netcup `159.195.12.230` only. Sub2API,
PostgreSQL, Redis, Caddy, and the cold standby were not restarted.

The release changes outbound Codex identity and stops automatic traffic from
administratively disabled accounts (`accounts.enabled=false` /
`DispatchPaused=1`):

- Pool and structured user agents no longer emit the terminal token `unknown`.
  A saved `unknown` value is shown as `xterm-256color`.
- Single-machine fingerprints keep session, thread, window, and request IDs
  distinct. Desktop personas send `X-Codex-Installation-Id`. Windows desktop
  attestation sends an unsupported-device envelope instead of an empty header.
- Official CLI/app version pairing already present on main was left unchanged.
- Disabled accounts no longer schedule usage, recovery, subscription, WHAM,
  credential-ops, channel, quality, model, connection, or intelligence probes.
  Manual admin actions for those probes return 400. Re-enabling an account
  resumes scheduling. The intelligence-check feature itself stays enabled.
- A 401-disabled Claude account and a quota-paused Codex account can still
  refresh tokens. An explicit admin token refresh can still contact upstream.
- Shared `/64` egress and customer Test-key prompt shape are infrastructure
  and usage facts. This release does not invent addresses or prompts.

## Provenance

Built from a clean `main` checkout after `origin/main` was fast-forwarded to
the task commit. `0671dbb7fda8cc5555f45233388604f9914133b2` is an ancestor.

- Source: `d12e0775399d2d0616767e8ba4b76472e3abf2a5`
- Tree: `2fdb9122bab0ab975d07f6d7e2aee1b62a9761ec`
- Upstream: `d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1`
- Build version: `release-20261102-d12e0775399d`
- OCI index: `sha256:c0813d2f4b8f3864dcf975b6b0a350856bd9f2acc88c215351f9e07fdfd48753`
- Loaded image/config: `sha256:181bcd4b1177cf3c3376e29c2e680ad20edf1d04bd079376af7ba786b84da75a`
- Binary SHA256: `163ab461c4a75c8546313799c0cefef17bc5c9e2f835aba764bf4cd1a8a74409`
- Container: `12a839910010579ce9b03003c2d4b11f327a15d7b0a436c786fc0323871c1713`
- Previous container: `98ea0eaa70f32dd7500ae333f1e227a4a7da6c7f4e852f29576e655812af471f`
- Rollback image of the previous executable: `sha256:33dbeff71a680ca807ccf54437f68e4e0dee1f03a73621f19e2cc771cf7ab1bf`

## Maintenance

`deploy_prepared.py` held `/var/lock/codex2api-release.lock`. Identity, source
baseline, image labels, and binary hash matched before stop. Isolated database
rehearsal passed. Only the Codex2API container was stopped.

- Maintenance on: `2026-11-01 17:41:48.128 UTC`
- Traffic opened: `2026-11-01 17:42:04.328 UTC`
- Recorded window: **16.200 seconds**
- Stopped backup: `/opt/codex2api/backups/client-identity-quiet-20261102-d12e0775/stopped.dump`, 9,903,877 bytes
- Release root: `/opt/codex2api/releases/client-identity-quiet-20261102-d12e0775/`

## Verification

Public and internal health returned `status=ok`, version
`release-20261102-d12e0775399d`, `available=0`, `total=7`. The credential
worker heartbeat was 2 seconds old after the check. Running `/proc/1/exe`
matches the released binary. Compose file remains
`/opt/codex2api/compose.netcup.json`.

Accounts 78, 79, 81, 84, and the other three non-deleted accounts stayed
`enabled=false`. After maintenance started, those four accounts had zero new
`usage_logs` rows and zero new intelligence runs. Their previous intelligence
runs remain at ids up to 274. Intelligence settings stayed revision 17,
enabled, interval 1800 s, recovery 3600 s. History floor stayed `[274,274]`.
No account was enabled.

PostgreSQL, Redis, Caddy, and Sub2API containers kept their previous start
times. `sub2api-sub2api-blue-1` had already started at `2026-11-01 15:40:44 UTC`,
before this release.

## Recovery

Use the same release lock and re-check container, image, binary, Compose, and
`/opt/codex2api/source-release.json` before any recovery. Keep the newest
database. Do not restore `stopped.dump` over later writes. The rollback image
above contains the previous executable; confirm it is still compatible with
the current schema before starting it.

## Not changed

The site egress pool remains a small set of addresses under one `/64`. Code
cannot make that look like separate household networks. Customer prompts sent
through the Test key are unchanged.
