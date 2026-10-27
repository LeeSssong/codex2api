# 当前生产交接：2026-10-27

额度移植、最新上游、定时降智检测恢复及netcup生产部署完成。权威简报 docs/releases/2026-10-27-quota-upstream.md。

- 已部署defac31e25eb，tree1cd8814f，上游3c6adfda，版本release-20261027-defac31e25eb。
- 主站159.195.12.230，维护12.603秒；检测enabled/600秒保持、历史7→8，凭证worker在线。
- 当前服务基线以服务器/opt/codex2api/source-release.json为准，禁止用旧共享目录HEAD或旧镜像标签推断。
- 先读AGENTS.md和docs/collaboration.md。原共享目录51文件保留且hash不变；分支e2517414，独立任务worktree开发，发布锁和源码基线拒绝旧候选。
- 已恢复写入，禁止旧dump覆盖。恢复入口见发布简报。本次无待部署功能，文档收尾commit无需重复构建或重启。

## 后续修复2026-10-27

已部署017294a8额度时间精度修复，见docs/releases/2026-10-27-quota-refresh.md；当前服务器source-release基线替代上述defac31e。后置保护误报已保留最新DB恢复新版，发布工具已排除用量计数。

## 当前本地任务：Sub2口径完整对齐（不部署）

用户明确授权本地实施且禁止部署。候选codex/sub2-quota-parity-20261027，基线9a24ddf4；实现和验证见docs/parity/sub2-source/IMPLEMENTATION.md。新增费用快照及精简费用表；生产仍为此前版本，本轮未推送远端main。旧共享工作区保持保留现场。

## 用户追加授权：历史值直接纠正

2026-10-27按用户明确要求已在线直接重算并回写8,255条现有account_billed及相关汇总；无新字段/备份/应用部署，用户扣费和APIKey字段哈希不变。见docs/parity/sub2-source/HISTORICAL-CORRECTION.md。应用候选仍未部署；本次数据操作不能当成新版本发布。

## 用户追加：用户计费用量同口径纠正

已在线直接纠正8,255条user_billed与account_billed一致，相关汇总及APIKey已用金额按差额同步；未改额度上限/凭据，无新字段/备份/部署。见docs/parity/sub2-source/USER-COST-CORRECTION.md。本地候选普通Codex Token共用最终成本价，数据库/代理完整回归通过；媒体单位价仍独立。
