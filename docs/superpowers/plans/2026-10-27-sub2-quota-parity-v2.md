# Sub2API quota parity implementation plan

**Goal:** Match the approved source accounting and silent-refresh semantics locally without deployment.

**Architecture:** Source-compatible immutable account-cost events feed native per-account window statistics. A last-successful-response cache controls the React quota view and atomic refresh.

**Spec:** docs/superpowers/specs/2026-10-27-sub2-quota-parity-v2.md

- [x] Fetch source baseline and establish isolated coordinator branch.
- [ ] Trace source account pricing/event admission; record exact conformance matrix with source lines/SHA.
- [ ] Implement per-row cost precedence, immutable storage and request plumbing; prove against source examples and preserve legacy/user-charge behavior.
- [ ] Align window starts, event inclusion, prediction edge cases and historical fallback.
- [ ] Port last-good snapshots, cache TTL, force refresh keys, batch dedupe and successful atomic replacement; browser compare first-load/loading/failure/reset/empty states.
- [ ] Integrate task commits, run affected verification, review accounting and async races, fix material findings.
- [ ] Record local commits, migration scope and parity evidence; leave production and remote main untouched.

Cost-affecting behavior tests are written before code. No source response is faked for a claimed production check. No broad retry of already-green unrelated suites.
