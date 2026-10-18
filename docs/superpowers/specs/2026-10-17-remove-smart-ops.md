# Remove customized smart operations

Approved in chat on 2026-10-17. Base: production revision 9d18793064af63e9533ab753a4b738e8c5d07a03. Work in existing checkout on codex/remove-smart-ops, preserving the untracked prior release plan.

Remove all seven customized smart-ops capabilities and plugin manager: frontend routes/navigation/import widgets/progress, admin and worker APIs, background jobs, request/scheduler/defaults/billing hooks and dedicated worker. Preserve official intelligence checks, hloolx model quality and IPv6, State, native BPS, normal quality tests, accounts, OAuth credentials, API keys and existing account/group concurrency values.

Delete dedicated settings/history/tasks and encrypted login inputs, plugin registry storage, smart_ops credential keys and quality_bps columns. Before dropping ownership snapshots, restore only automatic effects still provably owned and unchanged; do not blindly enable accounts or override later manual edits. Keep unrelated cooldowns and routing. Transactional idempotent migration supports SQLite and PostgreSQL, explicit tables without CASCADE, no startup recreation.

Production: inspect actual compose/container topology, stop application and dedicated credential worker before migration, deploy validated source preserving unrelated services. Remove dedicated worker service and exclusive secret configuration only. Verify health, removed APIs, retained capabilities and counts. Do not claim forensic erasure of historical backups. Summarize the October 16 integration (not merely v3.0.6) and UI paths.
