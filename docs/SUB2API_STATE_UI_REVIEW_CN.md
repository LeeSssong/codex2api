# sub2api 对照与 State 页面简化

核对日期：2026-09-18。核对官方仓库 `Wei-Shaw/sub2api`，main 提交 `efe9aab1e4ec89a42ba45e8dac20e882c5409a6a`。

## 实际查到的版本与实现

- 官方最新正式发布为 [v0.2.5](https://github.com/Wei-Shaw/sub2api/releases/tag/v0.2.5)，发布时间 2026-09-15；本次未查到用户提到的 2.6.2，不能把其他分支或 fork 的版本当作官方发布。
- [openai_codex_turn_state.go](https://github.com/Wei-Shaw/sub2api/blob/efe9aab1e4ec89a42ba45e8dac20e882c5409a6a/backend/internal/service/openai_codex_turn_state.go) 将上游 state 透传给客户端，按 API key 和客户端会话记录来源账号。在已知切换到其他账号时剥离回带的 state；未知来源保持透传。来源记录的 TTL 使用会话粘性策略，并非 token 有效期证明。
- 响应暂存后若发生 failover，只有真正提交给下游时才记录来源，避免旧请求污染归属。
- 本次核对的账号前端及此状态实现中，没有发现“按 292 长度轮换出口筛选并固定一小时”的同类管理界面。不能据此证明 292/312 与能力或限流有确定关系。
- [AccountsView.vue](https://github.com/Wei-Shaw/sub2api/blob/efe9aab1e4ec89a42ba45e8dac20e882c5409a6a/frontend/src/views/admin/AccountsView.vue) 将账号状态留在列表、受限原因放入详情弹窗；[EditAccountModal.vue](https://github.com/Wei-Shaw/sub2api/blob/efe9aab1e4ec89a42ba45e8dac20e882c5409a6a/frontend/src/components/account/EditAccountModal.vue) 按开关展开相关设置。这是本次借鉴的交互方式。

## 本地改动

- 默认「自动管理」：每个账号一行，Sol / Terra / Luna / GPT-6 Astra 并排显示状态和剩余时间。手机按账号排列模型，避免横向拖动大表。
- 「采集设置」集中账号、模型和出口；前置代理、Session、请求间隔等默认折叠。取消设置不会把草稿带到下一次开启操作。
- 迁移集中为「复制全部 → 另一台主机粘贴导入」，支持按账号或按模型复制，保留原始值复制。部分导入失败逐项显示，重试只提交失败项。
- 完整答题采集保留在「答题验证」中，两套管理入口不再同时堆在首屏。
- 关闭、过期、账号受限、等待和采集状态分别展示。关闭时仍可复制未过期值，但明确说明已停止采集与注入。

本次为界面与迁移操作改进，没有改变采集判定、账号/模型隔离、429 处理和 WS 连接复用实现。现有限制参见 [插件说明](STATE_292_PLUGIN_CN.md)。

验证包括前端单元测试、类型检查、生产构建，以及使用模拟接口的桌面/手机交互检查；没有为界面测试开启真实采集。
