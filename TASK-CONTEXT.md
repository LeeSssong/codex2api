# Current task: 2026-10-28 intelligence cooldown fix (not deployed yet)

Current live source remains 9aa624fb088a; authoritative /opt/codex2api/source-release.json.
This task branches from latest origin/main 69abb079. User approved automatic cooldown
pause/resume without rewriting manual switches/history, upstream integration with
conflicts preserving current running implementation, and deployment with preference
for no downtime. User subsequently explicitly requested direct downtime deployment (no additional
zero-downtime preparation). Use the proven locked maintenance entry and retain
required stopped backup/rollback safeguards; never overlap singleton writers.

Candidate details and checks: docs/releases/2026-10-28-intelligence-cooldown.md.
Owned worktree /Users/gongtengxinwen/.codex/worktrees/intelligence-quota-cooldown/codex2api,
branch codex/intelligence-quota-cooldown. Shared original directory is preserved,
not a release source. Upstream 3c6adfda already contained; re-fetch before deploy.

Current live detection observed enabled=true, interval600, revision12, history96;
older false/revision8 handoff is historical, never restore from it. Preserve live
settings and account enrollment at cutover. Never restore an old dump after new
writers have started; keep latest data and use compatible recovery/repair forward.
