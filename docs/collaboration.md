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
