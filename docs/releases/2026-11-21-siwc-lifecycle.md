# 2026-11-21 SIWC lifecycle — deployed

Production: https://codex.xingqiaolab.com on netcup 159.195.12.230.
Owner: Codex, branch `codex/siwc-route-isolation`, isolated worktree; base `511645b784db5abe8dad11cfd867ae7d7cc175c3`.

## Running source and artifacts

- Commit: `077be3decba972f77c4335fe504baa79511d4bd1`
- Tree: `00838ab48849d9d0b96cc113f5ee781329c56a47`
- Upstream ancestor: `4514623dda4ae025fde5af439345c1c0ba641a7c`
- Executable SHA256: `54d8e2791264e8d2c8ef38101bb48415dd2fff972500908b7fc20d77f806f5ac`
- Image: `sha256:473de1dd8d3065c7e238f3760464f0097102a26c2122886ec2ae844c9757291c`
- Container: `63b058e5d7993f95e0e0050350cb776c87fdb6fb87ad92ed467993fe959610d2`
- Compose: `/opt/codex2api/compose.netcup.json`; authority: `/opt/codex2api/source-release.json`.
- Artifacts: `/opt/codex2api/releases/siwc-host-20261121-077be3de/`.
- Stopped backup, migration rehearsal and receipt: `/opt/codex2api/backups/siwc-host-20261121-077be3de/`.
- Preserved credential runtime: `/opt/codex2api/releases/siwc-20261121-af50e9c7/runtime`; manifest SHA256 `a728f5ec308bc1297b268a1459d879846d8aa8a53fef0bc9a8eae13548db7ec9`. Worker online, SIWC capability present; encryption and mounts retained.

## Delivered behavior

Independent encrypted SIWC grants support browser PKCE authorization, saved password/TOTP login, refresh with lease/generation fencing, revocation and reauthorization. Standalone SIWC accounts do not require native tokens. Explicit SIWC selection routes inference to official Responses; native authorization serves quota/compact. No downstream API-key exclusivity binding was introduced. Native quota failures do not disable SIWC. Login and callback exchange freeze one upstream dialer with no direct fallback; browser-link login requires the browser to use that same egress. Host IDs use the required `urn:uuid:` prefix.

Unknown/new consent screens are handed to browser authorization; automated acceptance of the newly encountered registration form is not implemented. See `../siwc.md` for protocol limitations.

## Validation and actual remaining blocker

Full Go suite, targeted SIWC/database/auth/admin/tunnel race regressions, frontend typecheck/build and 534 frontend unit tests, 104 release-tool tests and runtime offline tests (16 Python, 3 Node) passed during implementation. Final Host ID change passed SIWC race and focused proxy/admin tests.

Real account validation passed password, TOTP and workspace selection and reached SIWC consent. A same-egress request to the normal consent registration endpoint returned HTTP 403 requiring organization-admin approval of app permissions, specifically setting **Use ChatGPT plan** to **Allowed** in Global Admin Console → External access. This is the observed provider response; no authorization grant was obtained. Account 86 remains disabled, SIWC unbound. Real SIWC inference and refresh remain unverified; native authorization separately requested email OTP, so native quota on this test account is also unverified. Do not report these as successful. Organization permission approval is the next dependency; then complete authorization and verify inference/refresh/native quota with valid grants.

## Deployment and preservation

All releases used the shared release lock and maintenance controller, exact baseline checks, stopped single-writer backup and migration rehearsal. Latest maintenance duration was 19.437 seconds; internal/public smoke, single writer and credential worker checks passed. Independent post-release inspection matched running executable and image against source receipt. Intelligence settings remained enabled, revision 17, interval 1800 seconds; 293 historical records and account flags were retained. No Sub2API, PostgreSQL, Redis or cold-backup restart was performed.

Earlier `af50e9c7` first release encountered obsolete `.top` DNS after internal smoke; locked recovery retained the current database and used the existing `.com` host. Its maintenance windows were 19.545 and 197.176 seconds. Follow-up `f6936c66` deployed standalone accounts with 20.206 seconds downtime. Both are historical, superseded by `077be3de`; never redeploy their candidates. All three implementation commits are merged/pushed. Ad hoc consent diagnostics are protected evidence, not application changes. Unrelated worktrees and unknown snapshots are preserved.

## Recovery and handoff

Latest rollback image: `sha256:25050496b9c5468a32ff182f6f440ca9e2baa54a91a0ff468998c6fd448c50e8`. Use the recorded release/backup artifacts and tested maintenance controller under `/var/lock/codex2api-release.lock`; recheck live identity and preserve current runtime/configuration and current database before any new release. New writes have been accepted: **never restore the old stopped dump over current data**. No old dump was restored in this task.

Local receipts: `~/.config/codex2api/siwc-host-release-20261121/final-verification.json`; earlier protected release directories retain baseline/build/diagnostic evidence. Credentials, cookies and authorization URLs are intentionally excluded from Git and this record. Owned local browser proxy/tunnel helpers were stopped; diagnostic containers exited and were removed. Documentation-only commits following this record do not change deployed application source.
