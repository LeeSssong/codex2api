# 额度移植、上游更新与并行协作

## 授权与来源

用户要求把生产 Sub2API 的额度费用与预测移植至 Codex2API，补齐 5h/7d；2026-10-27 追加授权合并 james-6-23/codex2api 最新 main、冲突以上游为准、一起部署，接受停机。

- 初始远端主线：84ec45973d94d142168d9d38bc6da9c912d2bd95。
- 本次上游：3c6adfda702efed67931b2d98a491d72bd9d91b5；发布前再核对。
- Sub2API 来源：a6d32edec667271d38c03ef55ac4a2edf77dfc5d，`upstream/sub2api/frontend/src/components/account/{AccountUsageCell,UsageProgressBar}.vue` 与 `backend/internal/service/account_usage_service.go`。
- 已核实来源只给 OpenAI OAuth 7d 预测：当前账号费用 × 100 / 官方已用百分比；5h 展示窗口费用。本次将同一公式延伸至有效的 5h 窗口。
- 目标生产：netcup 159.195.12.230；不可启动旧站或冷备业务。

## 展示与统计

账号列表及详情采用相同的 React 组件，5h/7d 各自展示「已用 $金额 / 预计总额 $金额」、百分比、进度、重置倒计时。次级显示请求数、Token、用户费用；费用来源和更新时间可见。官方结算单独保留，不能作为本窗口账号费用混算。5h 与 7d 重叠，不相加。长窗实际为 monthly 时标 30d，Spark 独立额度不套用普通窗口美元。

复用账号费用 `account_billed`，对应 Sub2API 的 `window_stats.cost`；不以用户费用或采购费用代替。按官方 reset 与实际 duration 算 `[start, as_of)` 窗口；所有本窗口费用、请求数和 Token 使用一致边界与既有有效日志筛选。当前页批量统计；不在页面读取时新增真实生成请求。

无数据、未加载、查询失败、窗口过期、0 消耗分别表达，不伪装成相同的 0。费用与官方百分比必须属于同一窗口；旧响应不能回填新窗口。样本百分比低于 10% 可展示估算，但标「样本较少」；百分比为 0 或非有限、费用无效、窗口缺失/过期时不预测。正金额不足一美分显示 `<$0.01`。

## 接口契约

在已有 account page-stats 响应中增加 `quota_5h`、`quota_7d`，旧字段兼容保留。列表账号详情若可直接复用同一 projection 则同步提供，详情也可使用 page-stats。

```ts
interface AccountQuotaWindow {
  label: '5h' | '7d' | '30d'
  state: 'ready' | 'low_sample' | 'no_usage' | 'unavailable' | 'stale' | 'error'
  reason?: string
  window_id: string
  start_at?: string
  reset_at?: string
  as_of: string
  observed_at?: string
  used_percent?: number
  used_usd?: number
  estimated_total_usd?: number
  estimated_remaining_usd?: number
  requests?: number
  tokens?: number
  user_usd?: number
}
```

`window_id` 包含账号 ID、真实窗口长度与 reset；前端比较账号当前 reset，不兼容则丢弃旧快照并显示待刷新。剩余预测为 max(0, total-used)。所有可选金额必须有限；缺失保持缺失。

## 并行与发布

原共享工作区 51 个已改文件完整保存在受保护的本地快照目录，原文件不改、不 reset、不整目录覆盖。本任务使用独立 managed worktree、codex 分支；实施人员使用各自隔离目录。已有未提交改动不得盲目打包发布；先按来源和最新代码逐项核对，冲突依用户要求以上游为准，并保留原快照。

永久规则写入 AGENTS.md 与协作说明。共用文件通过提交整合；所有主站发布共用 `/var/lock/codex2api-release.lock`。发布前及实际切换前核对预期的线上容器 ID 和 image ID，源 HEAD、tree 与 freshly verified origin/main 一致；候选包含本轮指定的上游提交。发现线上变化则停止旧候选发布，重新整合，不自动覆盖。首次遗留来源无法证明祖先时记录基线和处理，不伪造祖先关系。

先完成代码与前端构建、验证及镜像，再进入维护。若包含数据库迁移：停止唯一业务 writer，生成可检查的 PostgreSQL 备份，执行单实例迁移并启动；不得在恢复写入后以旧快照回灌。只更新 Codex2API；保留旧镜像、Compose、路由及恢复入口。线上核验 health、revision/tree/digest、额度 API 和页面；最终记录 main、制品、回滚入口与遗留项。

## 验收

用户追加明确要求：保留定时降智检测。检测开关、频率、计划调度、检测任务与历史结果、管理页面与 API 都必须保留；迁移前后核对实际数据库设置。不得仅保留前端入口，也不得将原先启用的定时检测静默关闭。

1. 算例：$12 已用、30% → $40 总额、$28 剩余；5h/7d 同公式。
2. 查询时间边界、重置切换、迟到响应、低样本、缺失/失败、0、小额、非有限值、月窗、Spark 分离。
3. 与 Sub2API 相同输入的预测一致；列表/详情一致；桌面/移动可读。
4. 上游更新的 Go 测试、前端类型/单测/构建通过；冲突或新增差异补相关验证。
5. 两个发布者互斥；基线变化拒绝发布；老候选不能覆盖新线上；原共享文件保持不变。

## 当前进度

- 已保存原工作区；已拉取双方远端；已在隔离分支以 `-X theirs` 合入上游，merge 成功。
- 后端、前端、发布保护与线上核验尚未完成；本文件不是完成证明。
