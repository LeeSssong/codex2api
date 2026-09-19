# Codex Turn-State 账号级复用

日期：2026-09-20

覆盖：codex2api（本仓库）、sub2api（`/Users/gongtengxinwen/Documents/sub2api搭建/upstream/sub2api`，官方 v0.2.7 + 星桥定制）

本文是设计规格。确认前不实施、不提交。

## 1. 目标

把 `X-Codex-Turn-State` 从「客户端回带的回合续链」改成「ChatGPT OAuth 账号的可复用能力票据」：

- 后台用轮换出口向 `chatgpt.com/backend-api/codex/responses` 采集合格票（个人 292 / Team 332）。
- 有效期内，该账号的 Astra 业务请求反复打这张票。
- 没票 = 没采集到，只走管理员配置的「没采集到」策略，不再另藏一套请求侧默认。
- 采集失败、存储不可用、注入逻辑异常时，宿主原出站路径仍可用（不引入 fail-closed 传输插件）。

## 2. 非目标

- 不安装、不依赖 `openai.oauth.protection_transport.v1` 插件，也不引入 Clash/SSH 采集机。
- 不采集、不注入 Sol / Terra / 其它模型；这些请求的 turn-state 头与今天完全相同，不额外增删。
- 不注入 compact（`/responses/compact` 及带 `compaction_trigger` 的压缩请求）；compact 路径同样保持今天的行为，不额外增删该头。
- 不增加额度、不绕过上游 401/403/429。
- 不把长度当成官方满血证明；292/332 只是入库规则。
- 不做上线前对照实验门。做完直接上，用真实业务观察。

## 3. 已否决的路径

| 路径 | 否决原因 |
|---|---|
| 原样安装 sub2api-state-reuse 保护传输插件 | 协议强制 `fail_closed`，进程挂了不回退宿主传输；会按两个固定组名改绑定 |
| 预处理插件只改头 | 不能改 `X-Codex-Turn-State`，也拿不到出站凭据 |
| 业务出口跟采集出口绑死 | 号池已有账号/组代理和粘性，绑死会打乱调度 |
| Astra 票打到 Sol/Terra | 社区明确禁止跨模型混用 |

选定路径：采集器（进程内后台任务）写共享票仓；宿主只在「范围内 Astra `/responses`」出站时覆盖该头。其它路径不碰这个头。

## 4. 共享语义

两端必须一致。差异只在存储、UI 和调度钩子。

### 4.1 票据

- 头名：`X-Codex-Turn-State`（HTTP）；WebSocket `response.create` 的 `client_metadata.x-codex-turn-state`。
- 解析：`base64url`，首字节 `0x80`，字节 1–8 为大端 Unix 签发时间，`(len-57) % 16 == 0`。
- 合格长度：`(292, 217)` 或 `(332, 249)`。312/356 以及其它长度一律拒收。
- 时钟：签发时间不得晚于 now+30s。
- 有效：`now < issued + 3570s`。
- 续采窗口：`issued + 3000s` 起，旧票在过期前继续可打。
- 隔离键：`account_id + model + credential_hash`。`model` 本阶段固定 `gpt-6-astra`。`credential_hash` 为 `sha256(access_token + "\0" + chatgpt_account_id)` 的 hex；刷新 token 后旧票自动失效。
- 一张账号同一模型只保留当前票。新票通过「采集 + 携票复验」后原子替换。晚到的旧响应不得删新票。

### 4.2 谁在范围内

账号同时满足：

1. ChatGPT OAuth（codex2api：非 relay / 非 API Key / 非 Agent Identity；sub2api：`platform=openai` 且 `type=oauth`）。
2. 属于至少一个「注入开关 = 开」的分组。

范围外的账号不采集、不注入、不触发没采集到策略。出站 turn-state 与今天完全相同（含现有跨账号回带守卫）。

### 4.3 哪些请求注入

仅当请求在范围内、当前票有效、且全部成立：

- 上游是 Codex `/responses`（HTTP 或 WS `response.create`）。
- 生效模型是 `gpt-6-astra`（含账号/分组映射之后的上游模型）。
- 不是 compact，请求体不含 `compaction_trigger`。
- 不是 `/models`、alpha/search、其它非生成端点。

注入动作（仅此路径）：覆盖出站头/WS metadata 为当前票。这是替换，不是把其它请求的头删掉。

下列路径**不做任何 turn-state 处理**，与今天完全相同：

- compact / 带 `compaction_trigger` 的压缩请求
- Sol / Terra / 映射后不是 Astra 的请求
- `/models`、alpha/search 及其它非 `/responses` 生成端点
- 范围外账号
- 范围内 Astra 但没票且 `miss_action=none`（不注入，头按今天规则透传或由现有跨账号守卫处理）

### 4.4 生命周期

```
缺票          每 ~20s、每轮 1 个采集出口
fresh         0–50min，业务打这张票，不主动采
renew_due     50–59.5min，每 ~5min 续采；旧票继续打
过期          票作废，进入缺票
```

采集请求：

- `POST https://chatgpt.com/backend-api/codex/responses`
- body：`model=gpt-6-astra`，短输入 `Reply with OK.`，`stream=true`，`store=false`
- 走采集出口，不走该账号业务代理
- 成功条件必须同时成立：HTTP 200、SSE 出现 `response.completed`、实际模型仍是 Astra、头为 292 或 332
- 同一出口立刻再打一次「携票复验」；两次都过才入库
- 全局采集并发上限 3；账号维度与业务并发分开
- HTTP 200 或长度合格单独都不算成功

采集失败分类：

| 结果 | 当前票 | 采集 | 账号策略 |
|---|---|---|---|
| 出口失败 / 312·356 / 流不完整 | 有则保留 | 换出口 | 仅当完全没票时走没采集到 |
| 429 / 额度 | **保留** | 至少 5 分钟或 Retry-After，不换 IP 硬撞 | 不触发没采集到 |
| 401 / 403 | 只作废本次打出去的那张 | 按当前凭据哈希暂停，换 token 后恢复 | 没票则走没采集到 |
| 采集进程/Redis 不可用 | 内存/磁盘里没过期的仍可用 | 起来后按缺票/续采接着跑 | 读不到票视为没票 |

不在请求路径上同步等待采集。

### 4.5 没采集到 = 没票

没有可打的有效票时，只执行下面这一套策略。没有「另外再默认普通转发」。

配置挂在功能级（不是两个固定组名），默认：

| 项 | 默认 | 选项 |
|---|---|---|
| 没采集到 | `none` | `none` / `rebind_group` / `unbind_groups` / `unschedulable` |
| 没采集到目标分组 | 空 | `rebind_group` 时必填 |
| 采到后 | `none` | `none` / `rebind_group` / `restore_schedulable` |
| 采到后目标分组 | 空 | `rebind_group` 时必填 |

含义：

- `none`：分组和可调度不变。已经选中该账号的请求**不注入**，turn-state 头与今天相同。这是 `none` 的请求侧含义，不是隐藏默认。
- `rebind_group`：解绑当前全部分组，改绑到指定组。本次请求**换号**，不拿这个号无票打出去。
- `unbind_groups`：清掉全部分组关系。本次请求换号。不限分组的 API Key 仍可能打到该号；真不想被选中用 `unschedulable`。
- `unschedulable`：标记为 turn-state 缺票挂起，调度跳过。本次请求换号。只恢复「因缺票挂起」的号，不恢复管理员手工停用的号。

状态只在有票↔没票变化时写一次，不每轮采集刷分组。

换号后若开启注入的号都没票、又都被移出，且没有范围外账号可接：返回现有「无可用账号」。不允许再抓一个没票号打穿 `unschedulable` / `rebind_group`。

429 且旧票仍有效：不算没票。

### 4.6 续链与 WHAM

出站注入的票**不得**当作「这是活跃回合续链」的证据。

- `ignore_usage_limit_status` / 粘性续传：继续只看入站客户端是否自带 turn-state、是否已有 session 绑定、是否有 `previous_response_id`。注入层改出站头之后，这些入站信号保持原样。
- Chat Completions 翻译路径客户端本来没有该头：新开回合仍按新回合调度，即使出站被注入了采集票。
- failover：每次尝试用**当前账号**的票；没有则按没采集到策略换号。禁止把 A 的票带到 B。

### 4.7 故障

- 票仓读失败：当没票，走 4.5。
- 注入代码 panic/错误：捕获后当本次不注入，走宿主原出站（fail-open）。
- 不把注入失败记成账号 401。
- 采集不得把业务代理切到采集出口。

## 5. 配置模型

### 5.1 功能设置（两端各一份）

```
turn_state_reuse_enabled          bool     总开关，默认 false
harvest_model                     string   固定 gpt-6-astra（只读展示）
harvest_proxy_urls                []string 采集专用出口；空则用现有代理池里标记为可采集的条目
harvest_use_proxy_pool            bool     默认 true：轮换现有代理池/IP 管理中可用 HTTP/SOCKS5
miss_action                       enum     none | rebind_group | unbind_groups | unschedulable
miss_target_group_id              int64?
recovered_action                  enum     none | rebind_group | restore_schedulable
recovered_target_group_id         int64?
inject_compact                    bool     默认 false，本阶段不做 UI，恒 false
```

总开关默认关：代码可以上线，分组打开且总开关打开后才采集/注入。用户要「做完就上」时，部署后打开总开关并给目标分组打勾即可，不必再等对照。

### 5.2 分组

新增一个布尔：`turn_state_inject_enabled`，默认 `false`。

- 仅 ChatGPT/Codex 渠道分组有意义；其它渠道保存为 true 时拒绝或忽略。
- 账号属于任一开启分组即进入范围。
- 关闭某分组只影响「因为这个组」进入范围；若还在别的开启组里，仍采集。

不自动创建「不降智」「降智」组，不按组名同步。

### 5.3 账号展示

账号列表/详情只读：

- 票状态：`out_of_scope` / `missing` / `fresh` / `renew_due` / `paused_auth` / `paused_429`
- 长度、签发时间、过期时间、剩余秒
- 最近一次采集结果（出口名脱敏、HTTP、是否 completed、失败原因码）
- 是否因缺票被挂起

不在列表展示完整票字符串。

## 6. codex2api 落地

### 6.1 新包

`internal/turnstate/`（或 `proxy/turnstate/`，与现有 `proxy/codex_turn_state.go` 并列、职责拆开）：

- `Parse` / `Accept`：形状与 TTL
- `Store`：按键读写当前票；Redis 优先，进程内缓存；Redis 挂了当没票
- `ApplyOutbound(...)`：仅在范围内 Astra `/responses` 且有有效票时覆盖该头；否则直接返回，不改 headers/body
- 不负责 HTTP 采集

现有 `proxy/codex_turn_state.go` 的溯源/跨账号守卫原样保留，继续服务所有路径。注入只叠加在范围内 Astra：有票则覆盖为采集票。没票且 `none` 时不调用覆盖，现有守卫照旧。

`ignore_usage_limit_status` 继续用注入**前**的入站 `codexTurnContinuationToken`。

### 6.2 采集

挂在现有账号探针/后台任务旁边的独立 worker：

- 只扫范围内 OAuth 号
- 出口：`harvest_proxy_urls`，否则 `harvest_use_proxy_pool` 为 true 时轮换代理池健康节点；都空则本轮跳过并记 `no_harvest_route`（不因此挡业务）
- 每轮每号最多 1 出口；缺票间隔 20s，续采间隔 300s
- 凭据从账号当前 access token 读，不另存一份
- 单实例用 Redis 锁或现有 outbox 选主，避免多副本同时采同一号

### 6.3 出站

- HTTP：`applyCodexAllowedForwardHeaders` 之后、真正 `Do` 之前，仅对范围内 Astra `/responses` 调用覆盖
- WS：同样只在范围内 Astra 的 `response.create` 覆盖 metadata
- compact、非 Astra、范围外：不调用覆盖，现有出站逻辑不动

调度：`NextAccount` / retry 循环里，范围内且没票且 `miss_action != none` 的账号直接跳过（等同换号）。`none` 时该号可被选中，出站不注入。

因缺票挂起：单独冷却原因 `turn_state_miss`，与 unauthorized/banned 分开。采到后若 `recovered_action=restore_schedulable` 只清这个原因。

### 6.4 管理接口与 UI

- 系统设置 Codex Tab：总开关、采集出口、没采集到/采到后动作、目标分组
- 分组编辑：注入开关（仅 Codex 渠道）
- 账号页：票状态列；详情里采集摘要
- `GET /admin/turn-state/status`：范围内账号摘要，供观察命中率、续采、401/429

设置项走现有 `SystemSettings` 读写，三语文案同步。

### 6.5 测试重点

- Parse：292/332 接受，312/356 拒绝，过期拒绝，时钟 +30s
- 注入：Astra 有票则覆盖；Sol/Terra/compact 的出站头与改造前相同；换号不用 A 的票
- 没票 + `none`：请求发出，turn-state 头与今天相同
- 没票 + `unschedulable`：该号不入选；无候选则无可用账号
- 429 保留票；401 只删当前票
- 续采失败且未过期：继续打旧票
- WHAM：出站有注入票时，新回合仍受用量窗口限制
- credential_hash 变化后旧票不可用

## 7. sub2api 落地

仓库：`/Users/gongtengxinwen/Documents/sub2api搭建/upstream/sub2api`，基线官方 v0.2.7。

### 7.1 不走插件

不上传 `.s2plugin`，不改保护传输路由。采集和注入都在主进程。插件挂了不存在这个问题：没有插件进程。

现有 `openai_codex_turn_state.go` 的跨账号守卫原样保留。范围内 Astra 有票时覆盖为采集票；其它路径不改这个头。

### 7.2 存储与采集

- 票：Redis（sub2api 已有）；账号摘要可写 Postgres 便于管理页，不以 DB 为热路径
- 采集 worker 在 backend 内，规则同 4.4
- 采集出口：功能设置里的 URL 列表，否则轮换 IP 管理中 `status=active` 且未过期的 HTTP/SOCKS5（只读，不改业务 `proxy_url`）
- 多副本用 Redis 锁

### 7.3 出站

在 OpenAI OAuth 网关构造上游请求、写入白名单头之后调用同一套 `ApplyOutbound`。WS 走 HTTP Bridge 的，桥接请求同样处理。

调度：范围内没票且 `miss_action != none` 则跳过该号。`unschedulable` 使用现有 `temp_unschedulable_*`，原因码固定 `turn_state_miss`，恢复时只清这个原因。

分组字段加在 `ent/schema/group.go`，新迁移，不要手改 ent 生成件之外的 SQL 而不走迁移。

### 7.4 管理

- 分组编辑：`turn_state_inject_enabled`（仅 `platform=openai`）
- 系统/渠道设置或独立「Turn-State」管理页：总开关、采集出口、没采集到/采到后、账号票状态表
- 不按组名自动搬家，不要求预先存在两个特定名字的组

### 7.5 测试重点

与 6.5 相同，外加：

- 非 openai / 非 oauth 账号不受影响
- 保护传输未启用时行为正确
- 票仓失败时宿主仍能出站
- 分组开关与账号多组隶属

## 8. 上线与观察

不做对照门。建议顺序（仍是一次上线，不是实验）：

1. 部署代码，总开关保持默认关，确认无注入、无采集。
2. 配采集出口（代理池或 URL 列表）。
3. 打开总开关，给目标分组打开注入。
4. 用真实业务观察：有票占比、292/332、续采成功率、没票策略触发次数、401/403 是否变密、多轮/工具/出图是否异常。

回滚：关总开关。关闭后停止采集与注入，出站 turn-state 全部回到今天的行为（跨账号守卫仍在）。因缺票挂起的号：关总开关时一并恢复 `turn_state_miss`，避免关功能后号被留在挂起。

## 9. 实施时文件（确认后才改）

### codex2api

- 新建 `internal/turnstate/`（parse、store、policy）
- 新建采集 worker，挂到 `admin`/`auth` 后台任务
- 修改 `proxy/executor.go`、`proxy/wsrelay/executor.go`、`proxy/codex_turn_state.go`、`proxy/handler.go` 调度跳过
- 修改 `database` 系统设置与 `account_groups`
- 修改 `frontend` Settings Codex Tab、分组、账号列表；三语 i18n
- 测试与上述包同目录 `*_test.go`

### sub2api

- 新建 turnstate 包与采集 worker
- 修改 `openai_gateway_service.go` / `openai_codex_turn_state.go` 出站
- 修改 `ent/schema/group.go` + 迁移、分组 DTO/前端
- 修改调度选号与 `temp_unschedulable`
- 管理设置页或独立页

## 10. 默认值汇总

| 项 | 值 |
|---|---|
| 总开关 | 关 |
| 分组注入 | 关 |
| 没采集到 | `none`（不注入、不换号、不搬家） |
| 采到后 | `none` |
| 采集模型 | `gpt-6-astra` |
| compact | 不采、不注入、不改头（与今天相同） |
| Sol/Terra / 其它模型 | 不采、不注入、不改头（与今天相同） |
| 采集并发 | 3 |
| 缺票间隔 | 20s |
| 续采间隔 | 300s |
| 续采起点 | issued+3000s |
| 过期 | issued+3570s |
| 合格长度 | 292 或 332 |
