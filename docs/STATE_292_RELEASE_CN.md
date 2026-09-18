# 2.9.8-state.2 发布说明

本版本发布到 `hloolx/codex2api` 的 `main`，镜像标签为 `ghcr.io/hloolx/codex2api:2.9.8-state.2` 与 `latest`，目标架构为 `linux/amd64`、`linux/arm64`。是否已经完成发布应以 GitHub Actions 的 Build Docker Image 成功记录为准。

## 本次变化

- 在 State 管理页加入默认关闭的 292 State 插件。
- 支持多个代理轮换、IPDeep 新 Session、可选前置代理，以及本机已绑定 IPv6 轮换。
- 按 OAuth 账号与精确模型串行采集。响应头到达后立即取消，HTTP 200 且长度、结构、时间符合条件才保存，不读取回答正文。
- 命中后固定保存值，按 token 签发时间加 3600 秒计算本地到期；到期停用并重新采集。401/403/429 进入暂停或冷却。
- 支持原值复制与跨主机迁移包，迁移按成员、工作区和精确模型匹配，不搬运 OAuth 或代理密码、不续期。
- 验证停用代理跳过、关闭时取消在途任务、重启持久化、账号/模型隔离和 HTTP/WS 消息字段注入。
- Docker 发布前验证 SQLite 启动、管理接口鉴权与插件配置持久化。

## 当前限制

插件按长度筛选，不验证回答质量，也不能确认响应正文随后是否返回 overload。292 长度不代表已证明的能力等级。

HTTP 与 compact 业务请求使用直连。WebSocket 消息字段会覆盖，但旧握手、连接池模型/State 版本隔离和续链旧出口的问题尚未修复；需要严格固定出口和 state 时，本版本应采用 HTTP 上游传输。

动态代理的新 Session 不保证不同 IPv6；目前未核实每次真实上游出口。HE 代理池未部署。没有执行真实账号的 292 捕获及跨主机验收。

详细使用方法见 [292 插件教程](STATE_292_PLUGIN_CN.md)，容器部署和更新见 [Docker 快速教程](DOCKER_STATE_QUICKSTART_CN.md)。

## 官方上游核对

2026-09-18 核对官方仓库 `james-6-23/codex2api`：当前最新 release 为 [v2.9.8](https://github.com/james-6-23/codex2api/releases/tag/v2.9.8)，`main` 相较本分支原有官方基线新增一条 [de41a5e3：CodexTurnState injection](https://github.com/james-6-23/codex2api/commit/de41a5e3dfe9ff51524af864532c42e37955346f)。

该提交新增每账号手动 State、模型名单/前缀匹配、HTTP/WS 注入、管理界面和用量追踪。每账号配置一个值，可匹配多个模型，并非按模型分别存储。其 `codex_turn_state_set_at` 从配置时刻起用于界面倒计时；`auth.Account.CodexTurnStateInjection` 不校验过期，也不解析 Fernet 签发时间。它没有自动采集、IPv6 出口轮换或 292 筛选。

本发布没有合入 `de41a5e3`：它新增的后置强制注入可能覆盖本插件的值，需要单独整合优先级与到期行为。上游现有的临时 429 退避和跨账号回声剥离已在本分支基线中；本次新增提交不是另一套 429 绕过方案。
