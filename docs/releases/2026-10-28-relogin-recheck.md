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

Status: preparing artifact; append actual identity/downtime/online checks.
