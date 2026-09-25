# Pelican source port implementation plan

> For agentic workers: execute this plan in isolated worktrees; verify directly relevant behavior before completion.

**Goal:** Port the archive's Pelican feature into Sub and its account quality/operations features into Codex2API, preserving archive behavior.
**Architecture:** Sub retains its native scheduled-test services and adds archive Pelican test options, history, showcase and UI. Codex2API adapts archive quality policy/judgment/scheduling and account operations notifications to its native quality-test/database/frontend stack as an optional built-in module.
**Tech Stack:** Go, PostgreSQL; Sub Vue/TypeScript; Codex2API React/TypeScript and PostgreSQL/SQLite.
**Spec:** User's final instruction of 2026-09-25: remove ALL custom requirements and implement the archive's features according to previously agreed system responsibilities.

## Binding constraints
- Source: /tmp/sub2api-2.8.11-design-review/sub2api-2.8.11 (archive sub2api-2.8.11.zip).
- No custom screenshot UI, browser renderer, record-only action, keywords/semantic rubric changes, extra quality email, manual-result isolation or custom interval controls.
- Preserve source Cron, account-bound scheduling, parallel outputs, quality judge reference-answer semantics, supported actions, recovery and alerts.
- No deployments, main changes, unrelated zip changes, real upstream calls or real emails. Existing local changes are untouched.
- Functional adaptation and relevant tests are required. Keep source attribution and license.

## Task 1 — Sub Pelican backend
- [ ] Compare source scheduled-test contracts/repository/service/runner and port Pelican-specific extensions only.
- [ ] Add Pelican execution, configuration snapshots, bounded output, leases, history endpoints and migrations with unused names.
- [ ] Adapt account test payload options, handler routes, provider wiring and showcase settings/API.
- [ ] Bring source behavioral tests before implementations, run focused tests and compile affected packages.

## Task 2 — Sub Pelican UI
- [ ] Port source Pelican manual account panel, scheduled controls and records dashboard.
- [ ] Port source user showcase/card/HTML sandbox/formatting, admin settings, APIs, routes, navigation and localized strings.
- [ ] Reuse current native components; verify source-target API parity, focused UI tests and typecheck/build.

## Task 3 — Codex2API account quality and operations
- [ ] Delegate independently in codex/account-ops-source-port; own all changes in that checkout.
- [ ] Reuse native account-bound quality jobs and discovery; add source-compatible persisted scheduled policies, judgments, actions/recovery/history.
- [ ] Add source-compatible balance/weekly alert classification, asynchronous events, SMTP configuration/delivery and admin pages.
- [ ] Register optional built-in settings/module using existing native auth/runtime/database/routes; no external plugin platform.
- [ ] Preserve PostgreSQL/SQLite portability and test real persistence, actions, retry/suppression and UI configuration.

## Task 4 — Review and delivery
- [ ] Review each implementation against archive scope; resolve integration defects.
- [ ] Run direct tests and builds, record any unverifiable items.
- [ ] Commit reviewed changes on isolated branches; report commits, checks and deployment status.
