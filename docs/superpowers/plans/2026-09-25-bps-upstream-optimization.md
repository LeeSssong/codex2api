# Codex2API Basispoints 上游优化实施方案（待批准）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. 本文只交付研究与计划；用户当前授权实施、部署的是智能运维。BPS 优化不随本次智能运维发布自动实施。

**Goal:** 在 Codex2API 原生路由、账号、计费、图片存储和智能运维上补齐 Sub 的 BPS 可管理能力，优先解决图片中转的可用性与通用 403 的错误处置边界。

**Architecture:** 复用 `proxy/codex_routes.go` 的路由决策、`account_codex_paths` 的账号路径权限、`CredentialGeneration` 的并发边界、`imagestore + image_assets + /p/img` 的图片链路以及现有运维事件/通知。新增设置和资源约束，不复制 Sub 的整个控制面，也不增加 sidecar、第二套账号或第二个账本。

**Tech Stack:** Go、Gin、SQLite/PostgreSQL、React/TypeScript、原生签名图片资源服务。

**Spec:** 用户补充请求及两张截图。Codex2API 基线为本次已发布 5de5d7531e6a6b6dbc9fcadb88c77c74724d96f8；Sub 对比基线为 b84cacb06bb6d5d71a46fccf1a09fe7e2acc2ef5。仅对直接相关源码作只读比较；本计划的 BPS 优化尚未实施。

## Global Constraints

- 一个 worktree 只有一个写入者；BPS 在智能运维发布完成后，基于最新 `origin/main` 新建 `codex/bps-upstream-hardening` 独立分支及 worktree。
- API Key 的 `codex_only`、`basispoints_only`、`basispoints_models_only` 以及能力筛选是权限边界；不得用图片或 403 回退绕过。
- 已输出内容、工具调用或 usage 的请求不得透明重放；不完整工具历史、密文历史、`previous_response_id` 继续遵循当前锁路机制。
- 403 不等于账号封禁；模型权限、路径健康、人工允许、凭证状态必须分别记录。自动关闭仅影响该账号的 BPS 路径。
- 关闭图片中转应同时停止新转换和访问已有 BPS 临时图；不影响正常生图资源。短期图片及其 base64、签名 URL、OAuth 凭证不进入普通日志。
- 复用原生请求内存预算，不再添加互不知情的第二套大请求排队器。图片容量不足立即返回 503，尺寸超限返回 413/400，均不触发账号惩罚。
- 涉及数据库结构的正式发布按项目规则做单套数据库停机迁移；其他编译型发布使用原生蓝绿链路。生产授权范围以届时用户明确批准的 BPS 方案为准。

---

## 一、结论：差距真实存在，但不是完全没有图片和 403 能力

| 维度 | Codex2API 当前源码已具备 | 相对 Sub 的差异及优化方向 |
| --- | --- | --- |
| 图片转换 | `proxy/basispoints_images.go:57` 已把消息和工具结果中的 `input_image` data URL 转为 HTTPS，单请求按 SHA 去重；`proxy/codex_routes.go:454` 在 BPS 发出前调用 | 补管理界面、热配置、数量/总字节/解码预算、严格格式与像素检查；不能再称“完全没有图片中转” |
| 图片存储 | `proxy/basispoints_image_host.go:20` 使用原生 imagestore，标记 `bps-inbound`；签名 URL 6 小时、文件 24 小时、复用窗口 12 小时 | Sub 为最后引用后 30 分钟、20 MiB/图、20 图且总 32 MiB/请求、1 GiB/512 张；建议采用 Sub 的短期资源合同 |
| 启用与可见性 | `NewBasispointsImageHost` 依赖 `IMAGE_ASSET_PUBLIC_BASE_URL`；设置页面 `frontend/src/pages/Settings.tsx:3607` 只有 BPS 总开关 | UI 中增加图片开关、HTTPS origin、实际状态及只读限制；当前 `frontend/src/locales/zh.json:4616` 仍写“不接受 base64 图片”，与源码能力不一致 |
| 图片访问与撤销 | `admin/image_studio.go:600` 校验 HMAC 和时间后提供图片；`internal/signedasset/signedasset.go:94` 缺显式签名密钥时按进程生成 | 未检查 BPS 临时图开关；公共读取继承 `public,max-age=86400`（`admin/image_studio.go:650`）；需独立 no-store、即时撤销、重启/双实例签名一致性 |
| 资源保护 | `security/validator.go:23` 默认入口 48 MiB；`security/request_memory.go:12` 默认共享 128 MiB 逻辑请求预算，HTTP/WS 已有限流 | 不是无限读入；但预算注释明确不含 JSON 拷贝，转换层只有 10 MiB/单图限制（`proxy/basispoints_images.go:28`）；需把解码/转换放大、图数、临时存储纳入同一准入体系 |
| 403 分类 | `proxy/codex_route_stream.go:33` 区分模型权限、上游访问、含糊 usage rejection、WAF 等；`proxy/basispoints.go:138` 对已规范化错误阻止通用惩罚/重试 | 缺“遇到通用 BPS HTTP403，仅关闭此账号 BPS”的可选持久策略。非 JSON/WAF 403 未规范成 request-scoped，可能落入通用 403 重试及 30 分钟 `payment_required` 冷却 |
| 账号与模型 | `admin/codex_routes.go:128` 已可逐账号/批量允许或禁止 native/BPS；`proxy/basispoints.go:39` 有环境模型白名单；Key 有五类明确路径策略 | 不是只有全局开关。缺账号级可编辑模型集合、继承/全选/空集合的明确语义，以及图中独立 403/缓存开关 |
| 能力与恢复 | `proxy/codex_capability_probe.go` 和 `auth/codex_routes.go:86` 已有模型证据、generation 隔离、路径冷却及恢复探测；有会话锁路 | 保留这些能力；“同步模型”只能同步目录，不能伪造某个账号已获权限，探测结果也不能覆写人工禁用 |
| 缓存计费 | `database/billing.go:347` 已有 cache read/write 的统一成本函数和 5m/1h 价格；BPS 已有独立响应适配 | 未发现截图对应的账号级“缓存创建按普通输入计费”开关；要同时规范 usage、账单、统计，不能仅改页面数字或把总输入重复加一次 |

### Sub 源码的直接依据

- `backend/internal/service/basispoints/image_relay.go:34`：30 分钟 TTL、1 GiB/512 张、20 MiB/图、32 MiB/请求、20 张、64M 像素；`:267` 重复引用延长资源寿命；`:421` 图片读取。
- `backend/internal/server/middleware/excel_bps_image_admission.go:15`：64 MiB HTTP body、32 个请求、预处理和保留 body 各 512 MiB、8 倍处理权重；这不是“保证 32/200 个最大图片请求并发”。
- `backend/internal/service/openai_excel_bps.go:235`：模型权限错误先单独返回；真正 HTTP403 才调用自动关闭；最终通用错误为 `basispoints_upstream_error`。
- `backend/internal/repository/account_repo_excel_bps.go:44`：原子核对凭证、OAuth 类型、当前 BPS 与 opt-in，只改协议开关并同步调度缓存。
- `backend/internal/service/account.go:2195`、`:2205`、`:2224`：缓存计费开关、403 开关、按映射后模型选择。
- `backend/internal/service/openai_excel_bps_usage.go:8`：保留总输入和缓存读，清空所有缓存写入别名及 TTL 细分，避免账单与下游 usage 分歧。

## 二、建议的用户体验与配置语义

继续放在现有“Codex 传输 / Basispoints 上游”内，分为“路由”“图片中转”“异常处置”，账号编辑及批量操作处提供账号覆盖值，不增加独立管理入口。UI 先做一页静态预览，审批后实施；本轮方案不提前修改界面。

1. **全局设置**：BPS 总开关、默认模型集合、图片中转开关、公共 HTTPS origin、验证状态。模型列表优先读取原生 BPS 目录与已有模型能力接口；环境变量作为首次配置的兼容来源，持久化配置保存后成为明确真值。
2. **账号设置**：沿用 native/BPS allowed；BPS 模型范围采用 `inherit | all | selected`，`selected + []` 明确不走 BPS；附带 `auto_disable_on_403=false`、`cache_creation_as_input=false`。只有常规 ChatGPT OAuth 账号可编辑。
3. **状态可解释**：展示“总开关关闭 / 账号人工关闭 / 403 自动关闭 / 模型未选 / 未证实支持 / 冷却中 / 可用”，以及最后原因、时间、来源。不把 `unsupported`、临时冷却、人工禁止混为一个红灯。
4. **图片诊断**：显示当前 origin、实际后端、已用字节/张数、过期清理错误、内存拒绝数和回退原因。只显示脱敏元数据，不列出用户截图缩略图。原管理员生图画廊当前查询包含 `bps-inbound`（`database/image_studio.go:537`），应排除此类临时输入资产；用户门户原本通过 job/API Key 过滤，不能声称其已泄露。

### 固定的首版图片合同

| 项目 | 建议值/行为 |
| --- | --- |
| 格式 | PNG、JPEG、GIF、WebP；校验实际魔数和可解码头，拒绝 SVG、BMP、伪造 MIME 与超过 64M 像素 |
| 单图 / 单请求 | 20 MiB / 20 张且总解码字节 32 MiB；对所有图片先预检与资源预留，避免第 20 张失败已留下前 19 张孤儿 |
| 临时存储 | 1 GiB、512 张硬上限，配额包含预留和待清理文件；多实例共享库/存储时原子预留，不能每实例分别突破同一磁盘总限额 |
| 生命周期 | 最后成功提交后保留 30 分钟；新签名 URL 最长 30 分钟，不能晚于资源到期；禁用立即撤销，重新启用不能复活已撤销链接 |
| 入口 | 最大 64 MiB，但取反代/服务更小的已配置限制；在现有 48 MiB 未提升前 UI 必须如实展示 48 MiB，不能只改说明 |
| 内存与并发 | 复用 `security` 原生预算，对编码 body、转换副本、decoded buffer 明确计费；最大 32 个读入/转换槽，实际并发取内存预算更小值；不把 Sub 的 1 GiB 内存预算原样套到当前机器 |
| 读取 | 原生流式读取；BPS 输入图 `Cache-Control: no-store`、`X-Content-Type-Options: nosniff`，不生成缩略图；限读取槽，不把文件整块读回内存 |
| 失败 | 413/400 表示请求超限/格式不合法；503 表示本地预算/磁盘不可用；仅在既有 Key 策略、会话历史允许且未联系 BPS 时，保持明确记录的 native 回退 |

## 三、分阶段实施

### P0 / Task 1：收敛 403 错误语义（先做）

**Files:** 修改 `proxy/codex_route_stream.go`、`proxy/codex_routes.go`、`proxy/basispoints.go`；必要时修改 `proxy/handler.go`、`proxy/responses_ws.go`、`proxy/continuous_retry.go` 的 request-scoped 判定；扩展 `proxy/basispoints_errors_test.go`、`proxy/codex_routes_test.go`。

**Interfaces:** 继续使用 `codexRouteFailure` / `codexRouteAttemptState`；增加可信 BPS 来源和“本次禁止重放”标记，来源必须由实际执行器/route attempt 设置，不能由用户传入错误文本决定。

- [ ] 在已有分类表上补用例：BPS JSON 普通403、HTML/WAF403、超出观察前缀的403、模型403、HTTP200 SSE文本包含403、签名图片本地403、native403、显式 safety rejection。预期普通 BPS403不影响 native 账号、不走 payment_required、不重试同一请求。
- [ ] 对可信 BPS 的通用 HTTP403 规范为 `basispoints_upstream_error`，保留原 HTTP status、内部类别和请求 ID；不将原始 HTML/body 或签名链接暴露给客户端。模型错误保留 `basispoints_model_access_changed`，safety/auth/billing/rate-limit维持原独立语义。
- [ ] 将该类别纳入 HTTP/WS/SSE/continuous-retry 的同一 request-scoped 排除路径。模型错误维持当前授权路由策略；未来启用自动关协议的普通403必须 `NoSwitch` 并禁止外层换号重放。
- [ ] 运行直接相关测试并提交：`go test ./proxy -run 'Test(Basispoints.*|CodexRoute.*)' -count=1`。验收“错误发生一次，账号 native 仍可用，未生成支付冷却，没有第二次上游调用”。

### P1 / Task 2：原生设置、账号策略与安全自动关闭

**Files:** 新增 `database/basispoints_settings.go`、`admin/basispoints_settings.go` 与对应测试；修改 `database/codex_routes.go`、`auth/codex_routes.go`、`admin/codex_routes.go`、`proxy/basispoints.go`、`proxy/codex_routes.go`、`proxy/runtime_config.go`。扩展原 `account_codex_paths` 和 system settings 持久化，不新建第二个账号策略真值。

**Interfaces（拟定，执行时在同一任务中实现与测试）：**

```go
type BasispointsAccountPolicy struct {
    ModelScope string // inherit, all, selected
    Models []string
    AutoDisableOn403 bool
    CacheCreationAsInput bool
    Revision int64
}
// 已有 allowed 是路径权限真值，不能另加一个重复的 Enabled。
func (db *DB) DisableBasispointsForHTTP403(
    ctx context.Context, accountID, expectedGeneration, expectedRevision int64,
) (changed bool, err error)
```

- [ ] 写数据库行为测试：凭证换代、撤销 opt-in、人工开关/修改模型后 revision变化、删除账号、并发重复403，旧请求均不得覆盖新操作；只有一条成功状态变更和事件。SQLite/PostgreSQL测试使用项目现有隔离fixture。
- [ ] 在一个事务中核对账号是适用 OAuth、generation、policy revision、BPS allowed与opt-in；成功时只改 `account_codex_paths` 的 BPS allowed，标记 `disabled_by=auto_403`、原因和时间，推进 revision。其他路径、账号状态、凭证不写。使用项目当前缓存/调度同步机制使更新立即可见。
- [ ] 把账号模型选择接在最终有效模型映射后、实际执行前，与全局能力目录及 Key 策略取交集。`selected=[]` 不得当成“全模型”。模型同步不写 capability=supported。
- [ ] 定义恢复：人工允许可清除 auto403状态；凭证刷新成功只让能力证据过期，不能自动解除人工/auto403关闭。质量守护与凭证守护不能复写此权限。
- [ ] 运行：`go test ./database ./auth ./admin ./proxy -run 'Test(Basispoints.*|CodexRoute.*|Codex.*Policy.*)' -count=1`。另用两个账号和一个 BPS-only Key 验证关闭一个账号不会偷偷切 native。提交独立迁移与策略变更。

### P1 / Task 3：图片中转热配置与完整资源生命周期

**Files:** 修改 `proxy/basispoints_images.go`、`proxy/basispoints_image_host.go`、`internal/signedasset/signedasset.go`、`database/image_studio.go`、`admin/image_studio.go`、`security/request_memory.go`、`security/middleware.go`；新增小型 `proxy/basispoints_image_budget.go` 和针对性测试。全局配置由 Task 2 管理。

**Interfaces:** 保持 `BasispointsImageHost.Host(ctx,data,mime)` 对上层契约；内部新增“按请求预检/预留/提交或释放”上下文。扩展 `image_assets` 的 BPS 生命周期元数据（`relay_expires_at`、`relay_scope_hash`、`relay_epoch`），复用现有文件与签名服务；设置的 `relay_epoch` 在撤销时递增，使重新启用也无法复活旧图。

- [ ] 先补失败用例：消息/工具输出 PNG/JPEG/GIF/WebP、10–20MiB、21张、32MiB+1、假MIME/高像素、磁盘满、中途取消、并发重复图、热禁用后旧URL、重新启用、重启/双实例、清理失败、HTTP/WS入口；既有 `TestBasispointsRewritesEmbeddedImagesInMessagesAndToolResults` 保留。
- [ ] 增加热生效设置：HTTPS origin严格不含凭据/path/query/fragment；只在BPS临时图路径应用独立origin，不无意改变生图画廊origin。默认继承已部署有效环境配置；保存后的显式禁用必须覆盖环境默认。
- [ ] 对整请求先收集图信息、计数、算字节与解码预算，再原子预留临时存储；成功转换后提交，任意失败释放。解码和JSON转换的内存附加占用通过原生 `RequestMemoryReservation` 计费；若需要暴露request上下文接口，在 `security` 内实现，不绕过原预算。
- [ ] 入站图片保持原生后端但从画廊查询排除；生命周期按最后引用更新。清理先安全删除对象、再删除元数据，失败保留待重试信息，避免现实现“删DB行后忽略文件删除错误”的孤儿（`proxy/basispoints_image_host.go:170`）。预留/待清理总量仍计配额。
- [ ] `/p/img` 在签名通过后还检查 `bps-inbound` 的enabled、epoch及expires；此类资源使用no-store、禁止缩略图、流式读取。配置统一受保护签名密钥和存储映射，验证蓝绿跨实例可读；不打印密钥。启用新合同前检查反代是否缓存旧BPS链接，有缓存则定向清除；服务端撤销不能收回已经被下载的副本。
- [ ] 跑：`go test ./proxy ./security ./internal/signedasset ./database ./admin -run 'Test(Basispoints.*|RequestMemory.*|ImageAsset.*|SignedImage.*)' -count=1`；只对新增并发存储/CAS用例执行一次 race。用一次可读文字的测试图片和一次工具截图完成 HTTP/WS→BPS→读图答复往返，核对URL到期/禁用。

### P2 / Task 4：缓存口径与智能运维整合

**Files:** `proxy/basispoints_usage.go`、`proxy/translator.go` 的原生 usage提取处、`database/billing.go`、`proxy/account_ops_observer.go`、`accountops/alerts.go`、`admin/account_ops.go` 及对应测试。最终与智能运维已发布接口对齐。

**Interfaces:** 在实际路径=BPS且账号 opt-in时才应用缓存创建按输入计费；先保存上游原始用量观测，再统一得到 billing/downstream usage。已有数据库账单、key扣费、usage聚合继续用同一个归一化结果。

- [ ] 使用固定合成用量 `input=1000, cached_read=100, cache_write=200, output=50`：打开后总输入仍1000、缓存读100、缓存写0，普通输入收费900；关闭时缓存写200按原写入价格，普通输入700。不得把 input 改成1200。覆盖5m/1h、别名、缺usage、SSE final、非流式与Chat/Messages转换，native路径保持原账单。
- [ ] 将 `auto_403_disabled`、图片容量拒绝、图片清理失败、BPS恢复建议接入现有事件历史和Bark通知；按 `accountID + credentialGeneration + category` 合并/冷却。图片错误只记计数和类别；不把临时图问题报告为OAuth失效。
- [ ] BPS模型支持/路由健康继续复用原生探测；不因普通403自动重登，不把自动关闭视为质量隔离解除，也不让质量守护恢复时重新打开BPS。
- [ ] 跑一次直接相关测试：`go test ./proxy ./database ./accountops ./auth -run 'Test(Basispoints.*|.*CacheWrite.*|.*AccountOps.*|.*TokenGuard.*)' -count=1`。核验billing结果、通知去重、旧generation无副作用后提交。

### P2 / Task 5：界面、端到端验收与发布

**Files:** `frontend/src/pages/Settings.tsx`、`frontend/src/components/CodexRoutes.tsx`、`frontend/src/components/AccountDetailSheet.tsx`、`frontend/src/pages/Accounts.tsx`、`frontend/src/api.ts`、`frontend/src/locales/{zh,en,zh-TW}.json`、BPS设置与账号策略测试、项目已有发布脚本及一份简短发布记录。

- [ ] 先给出与Codex2API现有风格一致的“全局图片设置 + 账号BPS策略 + 路由状态”预览；取得UI实施批准。保留当前设置页和账号页位置，明确只读限制与实际运行值。
- [ ] 实现配置读取/保存、账号覆盖/批量设置、当前配置来源、验证状态和事件入口。修正“完全不支持base64”的旧文案为“开启图片中转后支持；未配置时按Key和会话策略处理”。
- [ ] 执行 `npm --prefix frontend run typecheck`、直接BPS/账号设置测试和一次 `npm --prefix frontend run build`；在本地浏览器完成热配置、空模型集合、错误提示、手动恢复等交互，复用已通过后端证据。
- [ ] 汇总本次DB迁移。BPS方案获实施/生产批准后，干净根main与已核远端commit/tree一致才构建；不使用未提交worktree作生产源码。
- [ ] 若包含上述新schema，先说明中断窗口和迁移范围，准备可回退制品及已验证的DB备份/恢复方法；停止业务写入和相关任务，备份、迁移、启动单套新版本、必要冒烟后恢复流量。不能把切回旧镜像当DB回滚。
- [ ] 若某阶段仅应用层且无schema，沿用现有蓝绿流程：就绪→最小内部冒烟→切流→公网专项验收→最长300秒排空旧SSE。签名密钥与图片存储跨新旧实例一致，旧图URL在排空期间可读。
- [ ] 最小线上验收：文本一条、base64一张、工具截图一次、受控模拟403验证仅关闭BPS、一次BPS-only Key拒绝错误回退、一次对账。真实付费调用数量需保持最小；绝不为了制造403向上游发送滥用请求。
- [ ] 只写一份发布记录：目标、改动、commit/tree、镜像digest、复用/新增验证、阶段耗时、结果、回滚入口与未解决项。测试站未使用就明确未同步，不因此补做访问。

## 四、执行顺序及验收出口

推荐先完成当前智能运维生产发布；随后BPS按 **403语义修复 → 原生设置与账号CAS → 图片热配置及资源边界 → 计费/运维整合 → UI与专项验收** 实施。P0有独立价值，但如Task2–4紧接完成，可在全部直接相关验收后合并为一次正式BPS发布，避免重复迁移/停服。

验收不是“页面有两个新开关”：必须证明大图和工具截图真正由BPS读取，403不污染native账号与凭证，严格Key不发生未授权回退，缓存口径在账单/下游/统计一致，热禁用后旧图片URL不可访问，智能运维修复不会越权重开BPS。

**完成的核验：** 两端直接相关源码、已有测试与接口/数据库流向已只读核对；智能运维生产发布后再次确认：IMAGE_ASSET_PUBLIC_BASE_URL 未配置、持久 IMAGE_ASSET_SIGNING_SECRET 未配置，故现有内置 BPS 图片中转启动条件不满足。仅记录是否配置，未读取或输出密钥。未修改 BPS 配置、未执行真实上游读图、403注入或计费验证；这些属于获准实施后的专项验收。
