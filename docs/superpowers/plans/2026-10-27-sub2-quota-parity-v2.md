# Sub2API quota parity implementation plan

**Goal:** Match the approved source accounting and silent-refresh semantics locally without deployment.

**Architecture:** Source-compatible immutable account-cost events feed native per-account window statistics. A last-successful-response cache controls the React quota view and atomic refresh.

**Spec:** docs/superpowers/specs/2026-10-27-sub2-quota-parity-v2.md

- [x] Fetch source baseline and establish isolated coordinator branch.
- [x] Trace source account pricing/event admission; record exact conformance matrix with source lines/SHA.
- [x] Implement per-row cost precedence, immutable storage and request plumbing; prove against source examples and preserve legacy/user-charge behavior.
- [x] Align window starts, event inclusion, prediction edge cases and historical fallback.
- [x] Port last-good snapshots, cache TTL, force refresh keys, batch dedupe and successful atomic replacement; browser compare first-load/loading/failure/reset/empty states.
- [x] Integrate task commits, run affected verification, review accounting and async races, fix material findings.
- [x] Record local commits, migration scope and parity evidence; leave production and remote main untouched.

Cost-affecting behavior tests are written before code. No source response is faked for a claimed production check. No broad retry of already-green unrelated suites.

验收记录：docs/parity/sub2-source/IMPLEMENTATION.md；仅本地候选，未部署/未推送。

## 追加部署授权与完成

2026-10-27用户明确要求开始部署，替代此前仅本地约束。已发布9aa624fb，维护18.027秒；见docs/releases/2026-10-27-sub2-quota-parity.md。
