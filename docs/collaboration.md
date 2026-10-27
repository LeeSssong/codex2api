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
