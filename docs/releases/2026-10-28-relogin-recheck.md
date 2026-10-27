# Relogin state and intelligence recovery interval release

User authorized merge and downtime deployment on2026-10-28; target only netcup
159.195.12.230. Earlier no-deploy limit superseded for this release.

## Integrated source and verification

Credential candidate355d46e4 reconciles committed credentials/shared unauthorized
cache/runtime on completion/replay/retry, with generation-fenced SQL, atomic
cache cleanup and retired scheduler guards. Independent Excel/manual pause/quota/
monitor state is preserved.

Interval task had10 uncommitted files, snapshotted with hashes outside Git,
committed5aaf8ad5 and merged e0480e24; upstream20594822 included. Separate recovery
cadence defaults3600 seconds while intelligence-owned action is held; normal
cadence resumes after recovery. Legacy values normalize without rewriting saved
settings. Shared dirty original is untouched.

Merged full Go/race,458 frontend units, typecheck/build,9 browser tests,
85 release-tool checks and isolated LOCAL Redis Lua/CAS plus PostgreSQL
intelligence/credential lifecycle integrations passed. Local test containers
removed. Local Go compilation cache was rebuilt after disk-space exhaustion;
source, business data and protected evidence were preserved.

## Deployment preparation

Live source e61b04c3b9a72cbd75066ec09f7f5be7bf7f75cb;
container404f0d579a0e2e4d73c668eff2ca8e36521de6a5e199c67e7f3a1d387211f545;
image sha256:8e4fb65fb86fc0dbf1c6ce60639597c83f36bbe6a8c9dadbcc5a0c6ec03c2dab;
actual binary5bdc31033b1f3646267de6d1747450918c2c24f67426b2506aef773fc9ec77b2.
Compose /opt/codex2api/compose.netcup.json. Current settings revision13,
enabled=false, ordinary interval600 must stay intact.

Use tested maintenance_release.py under /var/lock/codex2api-release.lock,
clean pushed main source, exact identity and ancestry, rollback image, restored-DB
rehearsal, stopped backup, one writer, preserved credential runtime. Only target
Codex2API; no Sub2API/database/Redis/old-host/cold-standby restarts. No live login
probe or automatic enabling. Protected evidence:
/Users/gongtengxinwen/.config/codex2api/relogin-recheck-20261028/.

Status: deployed and independently verified. Documentation commits after the source below do not require another deployment.


## Deployed identity and actual maintenance

- Source: `884392a122b3abcaab74b1cd20cd05111aab06a3`; tree: `6f8aafa7f9fab4a1664de856502d2882f8e60a0d`.
- Upstream main at build/cutover: `20594822cde18233e812fd5ce3d279c598e75416`; it and previous production source are ancestors of the candidate. Latest origin/main was rechecked against the candidate immediately before dispatch.
- Version: `release-20261028-884392a122b3`.
- Executing binary SHA256: `bba286e114ece31d2a4f5d4517018c06572456ca1e31508119f3996cd8a7e4a9`.
- Actual image/config digest: `sha256:38e6e7997290eeb5642fe712751a54091e685a7e4a1ae800ee04785f764bfcd5`.
- Local build OCI index: `sha256:42f4d04396c31abf54111d61d99bbf5c12503b3e8c19111e815bd556543936c5`. The server classic Docker stores its platform config ID; both real identities are preserved and labels/binary were checked.
- Container: `eff66a0348d6d1cecd53786c7ad4f955769bae6c3d434d5865d2bad721162a2b`.
- Maintenance start: 2026-10-28T02:22:31.216+08:00; traffic reopened: 2026-10-28T02:22:44.124+08:00; actual recorded downtime: **12.907 seconds**.
- Final independent verification: 2026-10-27T18:27:38.888477+00:00.

Restored-database migration rehearsal and stopped backup validation passed. Exact saved intelligence settings, account switches, enrollment, credential runtime mounts/configuration and history were retained. No schema change was introduced by these tasks. Internal/public JSON settings, accounts/runs/quota APIs, admin pages, actual binary/image/source and fresh credential worker heartbeat passed. The new recovery interval is returned as3600 seconds; current detection stays enabled=false, ordinary interval600, revision13. History count/max ID is [104, 104]. Exactly one Codex writer is running; maintenance route and network gate are removed; protected services have unchanged container identities. No live login was triggered by verification.

## Backup and safe recovery

Release artifacts: `/opt/codex2api/releases/relogin-recheck-20261028-884392a1/`.
Stopped/preflight backups and evidence: `/opt/codex2api/backups/relogin-recheck-20261028-884392a1/`.
Stopped backup SHA256: `159f2391ec2bf75e94ef5460bdc7d990f064e64d3441f34eb731e5a78a866250`.
Exact previous-executable recovery image: `sha256:de6ff6975bca9594dbffe5d17db93303288dbab273aad5c4b59c8e6b8aa6a166`.
Protected local evidence directory above contains build/test/deploy logs and independent-final.json. The interval worktree's10 source-file hashes still match the initial snapshot; all unrelated worktrees remain preserved.

Production has resumed new writes. Do not restore stopped.dump or an older snapshot. For recovery hold the release lock, compare current source/container/image/executable/Compose, preserve latest data, stop the sole writer, then repair forward or verify old-binary compatibility with the current database before switching. compose.rollback.json is an artifact reference, not permission for blind rollback. Do not repeat this historical release. No Sub2API/database/Redis/old-host/cold-standby service was restarted.
