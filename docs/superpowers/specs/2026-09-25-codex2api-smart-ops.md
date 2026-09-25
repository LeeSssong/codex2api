# Approved implementation and production release

User confirmed: 确认，请你实施完后，部署到生产生效。 Execute implementation, tests and production deployment without another generic approval. The following audit records the pre-implementation baseline.

# Codex2API 智能运维完整集成：核查结果与待确认设计

状态：上游与线上版本检查完成；已生成交互预览。应用移植、数据库迁移、推送和部署尚未执行。

预览：同目录 `index.html`，仅使用明确标注的虚构账号和示例配置，不请求生产接口、不发信、不执行探活。

## 1. 已核实的现状

核查窗口：2026-09-25 21:19 UTC 起，对应北京时间 2026-09-26 05:19。所有“最新”仅指本次核查时刻。

| 项目 | 核实结果 |
| --- | --- |
| 用户指定上游 | `https://github.com/hloolx/codex2api`，默认分支 `main` |
| 上游最新提交 | `b402c6110ff3eab83c4ac4e9083366c0abc3c08d`；提交时间 2026-09-25 15:56:59 UTC / 北京时间23:56:59 |
| Release / Tag | GitHub API 返回空列表；不能编造一个语义版本号代表此分支 |
| 当前主站 | `sub2api-prod` → `64.83.10.67`；Codex2API 域名 `codex.xingqiaolab.top` |
| 正在运行的镜像 | `ghcr.io/hloolx/codex2api:latest` |
| 运行镜像 OCI revision | 与上游最新提交完全相同：`b402c6110ff3eab83c4ac4e9083366c0abc3c08d` |
| 运行制品 digest | `sha256:0c9eaca274963caaad775b292bc292912916003178056fb2138a49a8c79edd7a` |
| 健康状态 | 容器 healthy；本机 `/health` HTTP 200，`status=ok`、`build_version=main` |
| 智能运维API | 管理凭据认证下，`/api/admin/account-ops/module`、`/config`、`/alerts`，以及 `/api/admin/quality-ops/plans`、`/history` 均404 |
| 管理认证对照 | 相同凭据访问 `/api/admin/settings`、`/api/admin/stats` 均200；因此不是凭据错误造成缺失 |
| SPA路径说明 | `/admin/quality-ops` 等路径返回HTML外壳200，不代表功能存在；已以API和源码路由确认 |

结论：线上已经是本次查询时用户指定上游的最新源码，不需要重复拉取和重启。真正缺口是标准上游镜像没有本地此前定制的智能运维模块。

### 本地仓库与运行版本存在分叉

- 自有仓库：`/Users/gongtengxinwen/Documents/codex2api`，origin=`LeeSssong/codex2api`，upstream仍是`james-6-23/codex2api`。
- 本地main与实时自有origin/main一致：`4adc584779f223e4ce4b8db544b600e2312dc3e4`。
- 已有未提交修改三个文件：`frontend/src/locales/zh.json`、`proxy/grok_default_models_test.go`、`proxy/grok_models_registration_test.go`，本次未改动。
- 与hloolx共同基点：`de41a5e3dfe9ff51524af864532c42e37955346f`；hloolx独有37个提交、自有main独有77个提交。树差444个文件。不可当作普通快进升级，也不可整套覆盖proxy/database。
- Sub根main=`189d2efb8a`，本地显示领先origin18个提交；运行应用revision=`d8859f6949...`，两者树差仅发布报告。未修改Sub源码或运行态。

### 现存数据不会从镜像缺失推断丢失

- Codex数据库仍有旧定制的5张表：`account_ops_settings`、`account_quality_plans`、`account_quality_rounds`、`account_quality_recovery`、`account_ops_alerts`。当前plans/rounds/alerts计数均0；settings和recovery的具体内容未读取。
- Sub有4条scheduled_test计划、9条结果；其中quality配置的计划1条。质量状态、告警、凭证状态/事件/ownership计数均0。
- Sub的`account_ops_notifications_v1`、`account_token_guard_config_v1`配置行均不存在，已通过成功的COUNT查询确认。不能假定已配置可用SMTP、Bark、探活或重登凭据。
- 账号身份映射尚未执行；不存在“两个系统相同数字ID就是同一个账号”的假定。
- 未查询或同步独立测试站；未访问旧备用服务器。

## 2. 推荐方向及替代方案

**推荐：以最新hloolx源码为基线，原生集成智能运维三个模块。** 复用现成Codex账号运维移植成果，补上Sub最新质量恢复保护和完整凭证守护，使用Codex自己的账号、分组、模型目录、鉴权、调度、缓存、任务和审计链。前端沿用React和当前主题。

备选A：在外部保留一套Sub运维服务，调用Codex管理API。初期代码移动较少，但跨库事务、凭证版本、隔离所有权与恢复难保持一致，不符合“完完整整结合”的目标。

备选B：恢复旧Codex定制镜像。可找回旧质量/告警，但缺少凭证守护，而且失去当前hloolx BPS/路由/State改动；不是完整迁移。

本设计中的“智能运维”以当前Sub导航为准：质量守护、账号运维告警、凭证守护。保留Codex已有运维监控；不自动把退役relay-ops告警Agent、会计对账、渠道监控或主动余额飞书系统扩入此次开发。

## 3. 用户可见结构

- 在现有管理导航增加“智能运维”，内含质量守护、账号告警、凭证守护三个子页。
- 同一个管理员登录、同一套账号与分组；模型选项读取当前账号能力，不维护第二份账号库。
- 质量规则支持批量账号、Cron、1–8次并行、问题与参考答案、推理档位、判卷分组/模型/提示词、隔离动作、自动恢复、历史分页和详细回答。
- 告警页展示余额/周额度失败信号、通知合并、SMTP、冷却/重试/发送状态，保留已移植的质量处置/恢复通知。
- 凭证页展示范围、巡检、手动重登、自动重登/恢复、探活与重登服务配置、失效阈值、并发和单轮上限、邮箱密码/MFA映射、Bark和脱敏事件。预览只展示主要布局，实际功能不能因预览简化而省略这些字段。
- 控制台须明确区分“凭据修复成功”和“账号已恢复调度”；人工禁用、其他隔离或限流仍生效时，不显示已恢复。
- 中文、繁体、英文导航/状态复用现有i18n；遵守当前主题、组件、移动导航和权限结构。

## 4. 后端集成边界

### 质量守护

复用本地`accountops/`、`database/account_quality.go`、`admin/account_ops.go`及现成React页面，选择性接入最新源码。不得整文件覆盖旧版proxy/store/main。保留来源和LGPL许可。

- 执行与判卷使用原生`testConnection`、模型目录、账号并发槽与`quality_test_jobs`唯一任务约束。
- 被测账号不得自判；严格JSON只能是correct/incorrect/unknown。网络错误、缺少完成信号和不确定结果不触发隔离。
- 只有明确incorrect触发指定分组移除或禁用调度；全轮正确才满足恢复条件。
- 使用当前Sub版本的规则版本/租约/账号变更检查；旧移植中的恢复保护需逐项对齐，而不是只恢复旧两页。
- 对hloolx新增的Codex/BPS路径，记录真实检测路径、精确模型及凭据代次；不能把native结果当作BPS能力证明。

### 账号告警

复用失败信号分类和有界异步队列。挂钩最新所有实际出口，包括BPS及fallback，防止遗漏或重复观察。热路径不调用数据库或SMTP。

保留账号+类别合并、发送租约、失败重试、冷却和状态持久化。成功响应、普通限流、泛化unknown quota不误判余额/周额度故障。SMTP密码不回显；留空或掩码保留原值。

### 凭证守护

它是此次缺失最大的部分，旧Codex移植没有实现。源端调用外部探活/密码MFA重登服务，不是一个通用AI运维聊天Agent。

- 只处理符合条件的Codex/OpenAI OAuth账号；排除API中转、PAT、AgentIdentity、其他provider与删除账号。被守护隔离的账号仍需复检。
- 探活端点自身401、429/5xx、网络错误和未知文本均为暂态故障；只有认可的结构化token失效码累计失效。保留NDJSON协议、长度限制、不跟随重定向、字段白名单和脱敏。
- 若执行RT刷新，调用原生刷新协调器，保留RT指纹lease、持久化消费journal、credential generation与同RT多路由保护。
- 外部重登最长25分钟，原生刷新critical lease仅4分钟、Redis TTL5分钟，不能把整个长任务包进该lease。使用持久化长任务与独立fence；拿到新授权后才进入短发布阶段，重取锁、重读快照并CAS落库。
- 专用凭据替换事务校验账号身份、起始generation、账号控制revision、任务owner/取消状态；仅允许token白名单字段，保留workspace/路由/代理/分组。外部新授权不继承旧generation的BPS/模型能力证据。
- 为状态所有权增加可靠单调revision，覆盖人工及自动状态修改。credential_generation不能代表账号禁用/锁定状态版本，低精度updated_at也不能用于防ABA。
- 质量守护和凭证守护共用安全的账号动作边界。隔离与ownership同事务；恢复仅解除自己持有的变更。不得调用会清除全部限制的`ClearCooldown`，不得用刷新成功无条件激活账号。
- 写库后复用原生scheduler outbox，定点刷新本实例账号投影/缓存，其他实例从outbox同步；保留在途请求对象和并发计数。
- 多实例和手动/自动任务共用互斥；Postgres与SQLite都必须有等价的领取/fence语义，协调失败时拒绝写入。
- 管理接口全部挂现有adminAuth，不开放给普通API key或账号portal。事件不含邮箱密码/MFA/token/secret header/Bark key。密钥使用受保护配置；不声称当前可选加密已默认开启。
- Bark按现有能力迁移；源实现是直接HTTP通知，未实现持久重试，不能宣传成可靠投递队列。

## 5. 代码、数据与更新策略

1. 确认设计后创建独立`codex/codex2api-smart-ops`工作树。从最新自有origin/main的隔离分支协调hloolx基线与定制差异，最终集成树保留hloolx新增行为和逐项确认的现有定制；不强推、不重置、不触碰根目录三个未提交修改。
2. 选择性迁移质量与告警并适配BPS；随后接入完整凭证守护和共享恢复保护。
3. 对已有5张Codex运维表做schema检查，保留settings与历史；只补必要迁移。新增凭证states/events/ownership、任务状态及控制revision按实际实现形成迁移清单。
4. Sub唯一质量规则通过明确的账号身份、provider、模型与分组映射迁入；默认暂停。无法唯一匹配则保留待映射并报告，不凭数字ID导入，不自动复制全部账号、账务或凭据。历史保留来源标记。
5. 只有新端规则验收通过且账号映射明确，才执行源规则停用与目标启用，避免两端同时隔离/恢复同一上游账号；不删除Sub源码、历史或回滚入口。
6. 修正版本检查口径：上游`systemUpdateRepo`仍是`james-6-23/codex2api`，`main`构建的语义版本比较不适用。后续展示hloolx提交及本地集成提交，比较提交差异；不直接调用容器内二进制替换。
7. 定制生产镜像从自有已提交并推送的干净main构建并固定digest。以后更新hloolx也必须保留智能运维补丁；不能再用标准latest覆盖定制功能。

## 6. 直接相关验收

- 质量：并行回答、独立判卷、严格解析、取消/暂停/规则修改竞态、隔离归属、人工操作保护、BPS实际路径与模型证据。
- 告警：真实出口观察覆盖、嵌套去重、信号边界、热路径不阻塞、队列上限、冷却/租约/重试、SMTP掩码。
- 凭证：原生刷新与长重登并发、旧结果不可覆盖、RT消费fence不被清除、跨实例领取与重启、身份不一致拒绝、状态恢复不清除人工/质量/限流限制、generation/outbox/cache一致。
- SQLite与隔离Postgres验证迁移、CAS、事务及恢复。复用已存在且输入未变的回归证据；仅对改动补直接相关测试。
- 前端类型检查/构建，mock浏览器走完配置、启停、规则、运行/取消、历史、冲突及移动布局；不凭HTML200判断接口可用。
- 此轮预览只验证交互与排版；未执行应用Go测试、数据库迁移演练、真实模型调用或真实通知发送，不冒称功能已通过。

## 7. 发布与当前确认点

完整凭证守护包含数据库变更；正式发布按项目SOP采用单套停机迁移，不运行两套数据库。准备好可追溯镜像、备份恢复能力与本次迁移清单后，告知预计中断时间再执行。

正常顺序：干净且已推送main → 固定digest → 准备回滚镜像/配置与验证备份 → 停新写入并排空既有请求 → 一致备份 → 单套迁移 → 新应用就绪 → 版本/API/最小功能验证 → 恢复流量。Sub及无关DB/Redis/worker不重启。恢复流量后不能恢复旧数据库丢弃新增数据。

根Codex目录现有未提交修改不属于本次任务，发布前必须解决合规代码来源条件，不能代替用户提交或清理它们。具体处理在候选完成后按当时状态确定。

当前交付到“可评审设计与真实版本核查”。待确认的是：以此原生三模块结构和功能边界实施完整移植；不是询问是否允许已经完成的只读核查。确认前不把此预览接到应用或生产。

## 8. 直接证据定位

- Sub智能运维入口：`upstream/sub2api/frontend/src/components/admin/operations/SmartOpsNav.vue:10`，管理员路由`upstream/sub2api/backend/internal/server/routes/admin.go:789`。
- Sub质量/告警/凭证源：`backend/internal/service/{account_quality,quality_judge,account_ops,account_ops_signal,account_token_guard,account_token_guard_privacy,account_token_guard_safety}.go`及对应repository；路径均相对`upstream/sub2api`。
- 旧Codex移植范围：`/Users/gongtengxinwen/Documents/codex2api/docs/superpowers/reports/2026-09-25-account-ops-source-port.md`、`accountops/SOURCE.md`。
- hloolx只读源码：`/tmp/codex2api-hloolx-audit.HoTQLZ`，HEAD=`b402c6110ff3eab83c4ac4e9083366c0abc3c08d`。
- 当前导航/API：上述clone的`frontend/src/App.tsx:63`、`admin/handler.go:1103`。
- 当前更新源：`admin/system_update.go:31,444`；版本/容器判断`:299`。
- 原生刷新与CAS：`auth/codex_refresh.go:22`、`database/codex_refresh.go:88`、`auth/oauth_refresh_lock.go:14`。
- 原生outbox：`database/scheduler_outbox.go:188`、`auth/scheduler_outbox_consumer.go:338`。
- 实时GitHub API：`https://api.github.com/repos/hloolx/codex2api`及commits/main、releases、tags；`git ls-remote`复核最新HEAD。
- 实时线上证据来自定向docker镜像标签/digest、健康与管理GET、只读Postgres表结构/计数。未打印或归档凭据原值。
