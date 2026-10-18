# Remove Smart Ops Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for scoped implementation and review.

**Goal:** Remove customized smart operations and its dedicated data from current production while retaining upstream capabilities.
**Architecture:** Delete feature consumers, then dedicated implementation; run transactional one-shot cleanup with ownership-aware retirement.
**Tech Stack:** Go, SQLite/PostgreSQL, React, Docker.
**Spec:** docs/superpowers/specs/2026-10-17-remove-smart-ops.md

## Constraints
- Existing checkout; no worktree. Base 9d187930. Preserve untracked prior plan.
- No ordinary accounts, credentials, API keys, concurrency settings or unrelated service deletion.
- Independent frontend and backend ownership; database removal follows backend inventory.

## Tasks
- [x] 1. Frontend: remove SmartOpsNav and eight pages, routes, API helpers, TwoFAImport integrations and concurrency progress; remove obsolete dedicated tests/translations. Run frontend typecheck, tests, build. Preserve IntelligenceCheck and ModelQualityGuard.
- [x] 2. Backend: remove main/admin/auth/proxy wiring and dedicated packages; coordinate database symbols with migration owner. Preserve native routes and probe utilities shared with retained features. Run Go compilation and focused tests.
- [x] 3. Database: add failing old-schema upgrade tests; remove dedicated implementations and initialization; implement transactional retirement migration with exact table allowlist, credential-key cleanup, owned-effect restoration and repeated-startup tests. Validate SQLite and PostgreSQL.
- [x] 4. Integration: run complete Go and frontend suites, independent review, correct findings; remove worker tooling/configuration and record update usage guide.
- [x] 5. Release: inspect actual topology, build artifact from reviewed source, stop writers, migrate and replace only Codex services, remove worker exclusive configuration, verify deployment and retained data. Record source and verification evidence.
