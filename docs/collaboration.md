# 并行任务与发布交接

## 2026-10-27 当前整合

- 负责人：当前 Codex chat；目标额度预测移植、最新上游合并、定时降智检测恢复和停机部署。用户已授权合并、推送、部署及停机；代码冲突以上游为准。
- 主整合 worktree：`/Users/gongtengxinwen/.codex/worktrees/quota-upstream-20261027/codex2api`，分支 `codex/quota-upstream-20261027`。
- 后端：`codex/quota-backend-20261027`，独立目录 quota-backend-20261027/codex2api，仅拥有额度投影和边界统计相关文件；候选 `03b7a91c` 已整合。
- 前端：`codex/quota-frontend-20261027`，独立目录 quota-frontend-20261027/codex2api，仅拥有额度 React 组件/类型/页面与测试；发布由主整合负责。
- 发布审计/工具：release_audit 子任务只负责审计和离线工具，不得自行部署。
- 原共享现场已保存为 `codex/preserved-shared-20261027`，SHA `e25174149f7b87c0b5d36e7f7b3c0b5e778b6623`；受保护完整快照在本机 `.config/codex2api/quota-upstream-20261027`。
- 恢复版来源为独立历史 `recovered/credential-operations`。`codex/recovered-live-20261027` 是明确记录的重建祖先关系，不能称原始 Git 历史已恢复。
- 原现场旧身份/并发改动与上游新接口冲突处已选上游；原始内容仍完整在保留分支。Grok 模型相关测试保留并验收。

## 后续任务固定步骤

1. 阅读根 AGENTS.md；实时获取远端 main 和当前服务器身份，不从历史发布猜测。
2. 在独立 worktree 建任务分支；写明基线、拥有文件及验收。
3. 提交明确文件并把 SHA 交整合者；共享文件按提交整合。
4. 发布者先合最新 main，上线前复核远端 commit/tree 与服务器三项身份。
5. 持共享发布锁完成停写、备份、迁移、验证与恢复；成功后记录新基线。
6. 对已整合候选标记归档；不在共享脏目录强制 reset 或替换已运行程序。

规则文件只提供所有工具应遵守的入口；真正的并发保护还需要发布锁和预期版本校验。绕过统一入口的手工操作不受脚本保护，必须禁止。

## 2026-10-27最终部署基线

已部署9aa624fb088a，source-release.json为准。当前sub2-quota-parity分支已整合发布；原子任务提交为历史候选，禁止重复发布旧分支。定时检测当前关闭600秒，19历史保留。详见docs/releases/2026-10-27-sub2-quota-parity.md。

## 2026-10-28 Credential relogin state investigation (diagnosis complete)

- Owner: current Codex chat; branch `codex/credential-relogin-state-audit`; exclusive worktree `/Users/gongtengxinwen/.codex/worktrees/credential-relogin-state-audit/codex2api`; base `69abb079e1057952f2f93c7ac5920067a94d9f41`.
- User request: investigate successful Credential Operations reauthentication leaving the account unauthorized until a successful manual connection test.
- Scope: offline reproduction test in `admin/credential_operations_relogin_state_test.go` and investigation notes only; no production writes, real reauthentication, application behavior changes or deployment.
- Production source `9aa624fb088a425668004c45f1d2fa4868a49ee0`; relevant completion/reload/cache code is unchanged at the current main baseline. Other worktrees, including the active intelligence cooldown task, remain untouched.

- Result: missing shared unauthorized-cooldown invalidation confirmed by offline worker-completion/scheduler regression, repeated three times. Manual-test callback clears it. See `docs/investigations/2026-10-28-credential-relogin-state.md`. Regression intentionally fails; no application fix, push or deployment performed.

## 2026-10-28 Credential relogin implementation (complete locally, not deployed)

- User authorized implementation, explicitly no deployment. Continue in `codex/credential-relogin-state-audit` from diagnosis `6aed5588`; initial base `69abb079`; integrated remote main `20594822` after its intelligence-cooldown release.
- Owned files: `admin/credential_operations{,_runtime}.go`, `admin/credential_operations_relogin_state_test.go`, `auth/credential_ops_recovery.go`, `auth/{store,usage_cooldown_persist,transient_rate_limit,fast_scheduler,fast_scheduler_test}.go`, `cache/{cache,memory,redis,runtime_cooldown,runtime_cooldown_test}.go`, `database/{postgres,credential_operations_lifecycle,credential_operations_test}.go`, and task investigation/handoff documentation. Shared files integrate by commit only.
- Plan/results: `docs/investigations/2026-10-28-credential-relogin-fix-plan.md`. Diagnosis commit `6aed5588`, implementation `55dfa24a`, upstream merge `611c69fd`; final reviewed commit is the branch head after `611c69fd`. No push, deployment, real reauthentication or production account/data modification.
- Verified: three race repetitions across admin/auth/cache/database, full Go suite (24 passing packages), Go build and frontend build passed locally. Optional real Redis/PostgreSQL integrations remain unverified; no schema migration.
- Operational caveat: optional Redis verification accidentally used remote Docker context `sub2api-prod-build`; isolated no-volume `codex2api-relogin-test-redis` was immediately removed and its absence confirmed. User notified. No application deployment/service restart; real Redis/PostgreSQL integration is not verified. Do not repeat remote container/SSH operations for this local task.

## 2026-10-28 Intelligence cooldown task (deployed)

- Owner: current Codex chat; branch `codex/intelligence-quota-cooldown`; exclusive worktree `/Users/gongtengxinwen/.codex/worktrees/intelligence-quota-cooldown/codex2api`; base `69abb079e1057952f2f93c7ac5920067a94d9f41`.
- Scope: database/intelligence_check*.go, auth/intelligence_check*.go, admin/intelligence_check_runner.go and executor/integration tests, automatic-only proxy HTTP/WebSocket dispatch guards and tests, handoff and release records. The shared preservation directory is read-only.
- Latest user instruction overrides the old conflict policy: preserve the currently running production behavior if upstream conflicts. Upstream main is currently `3c6adfda`, already included; fetch again before publishing.
- Approved behavior: suspend scheduled checks during cooldown, resume when available, never rewrite manual switches or erase history. User subsequently requested direct downtime deployment, without extra zero-downtime preparation. Retain required stopped backup/rollback and never run concurrent old/new singleton writers.
- Acceptance: enqueue/claim/runtime fences, deadline and early recovery, manual switches/history preserved, SQLite/PostgreSQL, real periodic runner, and release-tool checks.

- Deployed source `e61b04c3b9a72cbd75066ec09f7f75cb`, tree `23d4b944ea0193fb07bc4b451ee5901954cf4e77`. Upstream `3c6adfda` re-fetched and already included. Later documentation commits do not require deployment.
- Production version `release-20261028-e61b04c3b9a7`, actual executable/image/source record verified. Settings enabled=true / interval600 / revision12 and history103 preserved; worker online and single Codex writer verified.
- Initial maintenance 16.491 seconds; external concurrent replacement of three Sub2API containers triggered protective recovery, adding approximately 184.702 seconds. Current data was preserved, no old dump restored, current non-target routing retained; traffic restored 2026-10-28 01:17:24.560 +08:00 and final public/internal verification passed.
- Release, evidence and safe recovery constraints: `docs/releases/2026-10-28-intelligence-cooldown.md`. Keep old shared worktrees/snapshots as preserved history; do not redeploy stale candidates. All 52 recorded shared-file hashes still match.


## 2026-10-28 Relogin + recovery-interval release (authorized)

- User now authorizes merging interval branch and downtime deployment; prior local-only restriction superseded. Interval10 dirty files snapshotted, committed5aaf8ad5, merged e0480e24; credential355d46e4 and upstream20594822 included.
- Full Go/race,458 frontend units, typecheck/build,9 browser,85 release checks and real LOCAL Redis/PostgreSQL integration pass. Live settings revision13/enabled=false/interval600 must remain intact.
- Use independent clean main clone, common release lock and maintenance tool; only Codex2API. Preserve shared dirty original. Result: docs/releases/2026-10-28-relogin-recheck.md.


## 2026-10-28 Relogin + recovery-interval release completed

- Both task branches merged/pushed; production source `884392a122b3abcaab74b1cd20cd05111aab06a3`, tree `6f8aafa7f9fab4a1664de856502d2882f8e60a0d`. Actual binary `bba286e114ece31d2a4f5d4517018c06572456ca1e31508119f3996cd8a7e4a9`; digest `sha256:38e6e7997290eeb5642fe712751a54091e685a7e4a1ae800ee04785f764bfcd5`. Source ancestry and latest main checked before deployment; subsequent docs need no release.
- Recorded downtime 12.907s; restored DB rehearsal/stopped backup, internal/public smoke and independent final verification passed. One writer, credential worker online, maintenance removed, protected services unchanged. Settings revision13/enabled=false/interval600, recovery3600, history[104, 104] retained.
- Release/recovery: `docs/releases/2026-10-28-relogin-recheck.md`. New writes accepted; never restore older dump or rerun stale release. Interval snapshot preserved; unrelated worktrees untouched. Earlier local-only/pending statuses are historical.

## 2026-10-28 web_search / Responses Lite WS isolation — local only

- Owner: current chat; approved implementation only, **no deployment**.
- Base: `d967cda1ae07bc948cb1dd0fcd791eb3e69d71c1`; branch `codex/websearch-ws-mode-isolation`.
- Worktree: `/Users/gongtengxinwen/.codex/worktrees/websearch-ws-mode-isolation/codex2api`.
- Scope: wsrelay local transport identity/pool acquisition/dial identity and regression tests; correction to proxy/executor.go's mixed-mode comment. No handler/store/frontend/account/settings/schema/monitor change.
- Plan: `docs/superpowers/plans/2026-10-28-websearch-ws-mode-isolation.md`.
- No production access or deployment is part of implementation; source remains a local candidate until separately authorized.

## 2026-10-28 web_search WS isolation — deployment authorized

- User now explicitly authorizes production deployment, superseding earlier implement-only/no-deploy restriction. Owner: this chat; isolated branch codex/websearch-ws-mode-isolation; candidate e0ecb08c; base d967cda1.
- Target netcup 159.195.12.230, Codex2API only. Preserve settings, credentials/runtime, database and Redis; use verified pushed main and common locked maintenance controller, exact live baseline and rollback artifacts. Release verification and deployment are in progress; earlier NOT DEPLOYED entries are historical implementation status, not a new deployment result.

## 2026-10-28 web_search WS isolation — deployed

- Latest user authorized deployment; source 2425875612935cc5244a7d844f27d09406e7bee8, tree fb12a3c54786eb9cb4547d56b8b0502c27464bc2, binary 2d7de25e4e296fa873b0849de92dd982acd4ab80bd6824cfe991f16938e815cc, image sha256:a94de947602e1d401bee620bf0ef146b597b113b6a79a6ec98fb7069aa0ac7e9. Fix e0ecb08c merged/pushed and deployed from clean current main. Earlier local-only status is historical.
- Maintenance 14.907s. Full Go suite, targeted race x3,85 release tests; internal/public smoke and independent final checks passed. One writer, credential worker online; exact settings/accounts/runtime retained; non-target services unchanged.
- Actual intelligence revision15/enabled=true/interval900/recovery3600, history[242,242]. All7 active accounts remain disabled by prior configuration: no real-account search smoke and no auto-enabling.
- Release/recovery: docs/releases/2026-10-28-websearch-ws-mode-isolation.md; protected evidence /Users/gongtengxinwen/.config/codex2api/websearch-release-20261028/; artifacts /opt/codex2api/releases/websearch-ws-20261028-24258756/, backups /opt/codex2api/backups/websearch-ws-20261028-24258756/. New writes possible: never restore old dump or rerun stale release. Original shared dirty checkout preserved.
