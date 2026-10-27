# Credential relogin state fix (local only)

User approved implementation on 2026-10-28 and explicitly excluded deployment.
Owner/worktree: current chat, `codex/credential-relogin-state-audit`, base `69abb079` plus diagnosis `6aed5588`.

## Design

The successful native Codex login commit must reconcile SQL, the shared unauthorized cooldown and runtime publication before returning success. Replayed completion and successful-task retries must retry the same reconciliation without consuming another login or clearing newer real failures. Excel grants do not authorize clearing the main Codex status. Quota cooldowns, manual switches and history stay intact.

Use a Store credential-recovery write gate around the short commit/reconciliation section; cooldown publication takes its read side. Retire replaced runtime objects and suppress their later scheduler/cooldown publication. Cache removal checks the reason and task completion timestamp and atomically compares the bytes, so changed/newer entries survive. Cache read/delete or reload errors return a recoverable failure while the durable task remains succeeded. No schema migration or frontend changes.

## Implementation and verification

1. Keep the existing end-to-end red regression; extend coverage for late old-object 401s, completion replays/retries, cache outages, Excel grants, newer auth failures, unrelated quota cooldowns and manual switches.
2. Implement a narrowly scoped Store recovery/publication gate and conditional cache cleanup. Route successful completion and replay/retry through a shared admin recovery helper; preserve existing task/credential generation fencing.
3. Run targeted red/green tests, repeated/race tests on the affected paths, package checks and build. Review the diff for new state clearing and callback error handling.
4. Update investigation/handoff with results and a local commit. No push to main, server writes or deployment.

## Review focus

- Old in-flight publication ordered before commit or discarded after replacement.
- No unconditional Redis deletion and no broad cooldown reset.
- Replays cannot erase a newer credential-generation failure or unnecessarily reload an already current runtime account.
- Successfully saved task is not turned into a failed/requeued login when runtime reconciliation is pending.
- Preserve independent Excel grants and manual pause/monitor/auto-login controls.
