# Quota parity and upstream release implementation plan

**Goal:** Ship the approved quota presentation and prediction together with latest official upstream without overwriting concurrent work.

**Architecture:** Use native current-page billing queries and a typed per-window projection; shared React quota component; immutable build and a serialized server release with expected-current-state checks.

**Spec:** `docs/superpowers/specs/2026-10-27-quota-upstream-design.md`.

## Execution

- [x] Preserve original shared worktree files outside Git. Fetch origin/upstream and merge upstream into an isolated codex branch, choosing upstream conflict hunks.
- [ ] Backend: add the spec's quota_5h/quota_7d projections, aligned current-page aggregation, missing/error/window metadata and meaningful boundary tests. Own admin/account_quota_windows*, admin/account_page_stats*, and necessary database bounded query files only.
- [ ] Frontend: add AccountQuotaWindow type and reusable account quota component, consume page-stats in list and detail, guard outdated window results, localize and verify formatting/state behavior. Own frontend only.
- [ ] Release: adapt build/maintenance deploy to the netcup target; add tested shared-lock and expected-current-image/container checks, preserve all unrelated containers and protected configuration; persist collaboration instructions.
- [ ] Integrate independently committed candidates; review affected billing, API, UI and deployment behavior. Run relevant Go and frontend gates plus upstream merge regression once toolchain is ready.
- [ ] Merge to fresh origin/main with fast-forward protection and push; prepare immutable frontend/backend/image from clean pushed main and validate source tree/digest.
- [ ] Under the server lock, verify current baseline again, stop Codex writer, snapshot/verify backup, migrate only if required, start and validate. Record downtime, source, artifact, restored traffic and rollback entrance.
- [ ] Preserve or archive original task changes and obsolete candidates with provenance; document the next-agent entry point and verify original shared files untouched.

Each implementer writes behavior tests first, confirms the intended failure, implements, reruns direct tests, and returns a commit plus evidence. No agent deploys except the primary coordinator. Credentials remain only in protected files; no output of raw account records or secrets.
