# Key 按模型强制 BPS

`basispoints_models_only` 用于“BPS 提供的模型只走 BPS，其余模型才使用原 Codex 或其他账号”。旧的 `basispoints_prefer` 行为不变。

## 启用

先部署支持该策略的新版本，再编辑目标 Key → 高级限额 → Codex 双上游路径：

- 路由策略：**BPS 模型强制专用**。
- 能力筛选：**任意能力（含未知）**。此策略对 BPS 自带精确模型验证，仍会拒绝未知账号；无需再选择“BPS 已确认支持”，否则该额外筛选也会限制名单外的原生模型。
- 账号渠道：**自动**允许其他已授权渠道；**Codex**只允许 Codex 账号。
- 保留 Key 的账号组、套餐、模型白名单和额度设置。

对应 limits 字段为 `codex_route_policy: "basispoints_models_only"`、`codex_capability_filter: "any"`。编辑 API 的 limits 对象会整体保存，脚本更新前应读取并保留其他限额，不能只提交这两个字段覆盖已有权限。

全局 `CODEX_ROUTE_POLICY=basispoints_models_only` 可供“继承全局默认”的 Key 使用；显式 Key 策略优先，不自动修改其他 Key。

## 模型与账号判断

默认 BPS 名单为 `gpt-5.6-sol`、`gpt-6-astra`，可通过 `BASISPOINTS_MODELS` 显式覆盖；匹配大小写不敏感，也识别有效日期快照。`*` / `all` 表示所有模型均强制 BPS。名单是管理员维护的模型提供范围，不是根据临时错误自动推断的在线目录。

全局模型映射按实际模型判断；中转账号自己的映射也不能把 BPS 名单内模型绕到其他渠道。名单内模型要求账号具有当前凭据对应、该精确模型的 BPS 成功观察。其他模型的成功、账号级笼统成功、未知能力都不满足要求。先在账号管理中对要使用的精确模型运行 BPS 强测试；过期凭据更换后需重新确认。

| 情况 | 行为 |
| --- | --- |
| 名单内模型，有已验证且可用的 BPS 账号 | 仅 BPS，继续遵守账号组、模型、额度和并发限制 |
| 403、限流、配额耗尽、单个账号无模型权限 | 仍限制在 BPS；可按原重试规则换其他合格 BPS 账号，不能转原生或中转 |
| 无已验证账号 | 503，排除原因包含 `exact_model_unverified` |
| BPS 路径冷却 | 503 `codex_route_cooldown`，返回 Retry-After |
| 全局 BPS 开关关闭 | 503 `codex_route_basispoints_disabled`；不解释为模型不在名单 |
| 名单内模型请求 BPS 不兼容的联网搜索、输出格式等功能 | 400 `codex_route_basispoints_protocol`，移除不兼容功能或使用另行授权的原生 Key |
| 模型不在 BPS 名单 | 可用原 Codex 或 Key 原本允许的其他账号；仍检查原生 State 等要求 |
| 携带已知来自其他上游的不可回放历史 | 明确的历史策略冲突；恢复允许原路径的 Key 或新建会话 |

HTTP Responses、Chat Completions、Messages、Responses WebSocket 与 compact 共享选路规则。请求体不能覆盖 Key 的路由策略。

## 验证范围

回归覆盖 BPS 精确模型证据、凭据代际、分组隔离、全局/账号模型映射、上述各入口、HTTP/SSE 403、401、429、配额与模型权限错误、禁用开关、功能不兼容、名单外原生/中转、原生 State 及历史来源冲突。浏览器测试检查桌面和手机界面的选择、保存和重新打开。测试使用本地模拟上游，不消耗生产账号额度。
