# Codex2API：归档降智运维与账号告警移植

日期：2026-09-25。状态：实现候选完成，本地验证；未推送、未部署，不标记线上 DONE。

## 范围与来源

- 独立工作树：`/Users/gongtengxinwen/Documents/codex2api/.worktrees/account-ops-source-port`。
- 分支：`codex/account-ops-source-port`；基线：`8020e25757e0b7f3eda882c447c514b614a1b8e3`（领取时最新 `origin/main`）。
- 原始归档：`/tmp/sub2api-2.8.11-design-review/sub2api-2.8.11`。移植行为以归档实际代码为准。
- 原始文件：`backend/internal/service/{account_quality,quality_judge,account_ops,account_ops_signal,openai_images_balance}.go`、`backend/internal/repository/{account_quality,account_ops}.go`、迁移 247/248/249、`frontend/src/views/admin/{AccountQualityView,AccountOpsView}.vue`。
- `accountops/SOURCE.md` 记录出处，`accountops/LICENSE.LGPL-3.0` 保留完整归档许可证。
- Basispoints 工作树仅用于阅读内置可选开关模式；没有合并该分支或引入外部插件系统。Sub/Pelican 由父任务负责。

## 行为对照

| 归档行为 | 原生实现与验证 |
| --- | --- |
| 每账号最多一条质量规则 | `account_quality_plans.account_id` 唯一约束；真实 SQLite/PostgreSQL 重复创建测试 |
| 五段 Cron；每分钟扫描；立即检测加入下次扫描 | robfig 五段解析；按程序 `time.Local` 解释，再以时间戳持久化；手动触发拒绝忙碌租约；Asia/Shanghai 09:00 回归测试 |
| 1–8 次并行回答；自定义问题、参考答案 | 每轮并发执行；默认糖果题和答案 `21`；问题附加原始“只输出最终答案，不要解释。”；双请求屏障测试证明并行 |
| 选择判题分组、模型、提示词；模型无固定默认 | React 编辑器使用原生模型/推理档位发现及预设 API；判题 prompt 原样沿用语义比较、数据与指令分隔和 JSON 输出约定 |
| 判题账号不能自判；占用槽；失败至多尝试 3 个账号 | 原生分组成员、账号可用性、模型目录筛选；原生调度器选择并占用/释放账号槽；最多 3 次；测试检查被测账号不参与判题 |
| correct / incorrect / unknown 严格解析 | 归档严格 JSON 解析器；拒绝多对象、重复键、未知字段、无效 verdict、空理由；只有完成且明确 incorrect 触发动作 |
| 网络失败、空白/未完成响应、不确定不触发动作 | 复用原生 `testConnection` 和事件 sink；空白/未完成流测试；原生输出上限；传输错误判不确定 |
| 取消目标分组或关闭调度 | 仅 `remove_groups` / `disable_scheduling` 两种动作；规则状态、账号变更与原生调度 outbox 触发器同事务 |
| 后续全轮通过自动恢复，默认关闭 | 恢复本规则实际移除的组或实际关闭的调度；源代码的 active、订阅有效期、组存在性检查；已存在组 `ON CONFLICT DO NOTHING` |
| 暂停、编辑时旧结果不变更账号 | 数据库版本/启用状态/租约校验；软删除账号在领取及应用阶段排除；源代码状态标签与历史保留 |
| 恢复保留人工增加的其他组 | 与实际归档代码一致：不比较隔离后账号版本/剩余成员；仅恢复本规则持有的变更。归档说明文档对“版本冲突”描述已过时，未照搬那项额外限制 |
| 全局运营记录、详情按需加载 | React 规则库与轮次表；游标翻页；详情展示每次输出、理由、判题账号/模型；响应只按文本展示，无浏览器渲染 |
| 历史保留数量和 7 天清理 | `max_results` 默认 100，上限 200；按样本数而非轮次数裁剪；周期清理即使规则暂停也执行；分页后暂停自动刷新，避免已加载页面丢失 |
| 余额不足和明确周限额邮件 | 归档 JSON/文本/嵌套结构与 Codex 七天、Anthropic 七天头判定；普通限流、泛化 insufficient_quota、HTML、成功响应和过大错误体忽略 |
| 异步有界队列、合并、冷却、重试和状态 | 256 槽内存队列；账号+类型唯一行；2 分钟发送租约；默认 60 分钟冷却、可设 5–1440；前 2 次失败 5 分钟重试，第 3 次进入冷却；抑制/失败/发送状态持久化 |
| 热路径不访问 DB/SMTP，不保存原始错误或凭据 | 错误 body 包装器随原有调用方读取，最多收集 32 KiB+哨兵；只入队分类后的信号；原生账号名称在异步 DB 消费者补齐；嵌套执行器去重 |
| 覆盖实际上游失败 | Codex、Responses/compact 中转、Claude、Grok Responses/原生协议、Antigravity Responses 与原生 Gemini generate/countTokens 出口挂钩 |
| 邮件配置与后台入口 | 受原生管理员认证保护的配置 API；TLS/STARTTLS SMTP；密码不回显，留空保留，沿用原生可选凭据加密机制；模块默认关闭，关闭时捕获与发送均被门控 |

## 原生适配与边界

1. Codex2API 的 `account_group_members` 只有账号/组 ID，没有归档独立的成员优先级、成员模型限制或创建时间字段。因此恢复的是完整原生关系；判题优先级由原生调度器负责，模型资格由原生账号模型目录负责，没有伪造不存在的平行字段。
2. 每个调度轮次复用原生 `quality_test_jobs` 的一个容量槽与账号唯一任务约束，详细并行样本/判题/动作存入专用轮次表。原生手动检测忙碌或全局容量满时延期，不伪造失败轮次；领取前先清理崩溃过期的原生任务。
3. 原生取消入口可以取消上述轮次：状态 watcher 取消正在请求的回答/判题；数据库动作事务再次锁定并检查原生任务状态，取消先于应用动作时不会隔离账号；最终任务为 stopped，轮次记录 cancelled，租约释放。
4. 原始判题请求使用 medium。Antigravity 原生公开模型 ID 已编码推理档位、只接受空 effort，因此该渠道不额外传 effort；OpenAI/Claude/Grok 保留 medium。两者均有直接回归测试。
5. 原生质量测试已有 HTML 指令。新增内部 `TextOnly` 标志仅供此模块的文本回答/判题调用，普通质量测试请求和页面不变；不新增外部 record-only、语义关键词规则、质量邮件或自定义间隔控件。
6. 页面实现为原生 React/现有 Dialog/Button 和主题变量，保留源规则库、运营表、侧栏编辑/详情、邮件配置与事件表的功能结构；正文当前为中文，侧栏导航包含原生中英繁体翻译。没有复刻先前取消的自定义截图方案。

## 文件

- `accountops/`：归档告警服务、信号分类、质量 DTO/策略、Cron、严格判题解析、SMTP、来源与许可证、单测。
- `database/account_ops.go`：PostgreSQL/SQLite 启动迁移、可选模块设置、SMTP 配置、告警合并/领取/完成/抑制。
- `database/account_quality.go`：规则 CRUD、租约、触发、延期、动作事务、恢复基线、轮次历史与清理。
- `database/account_ops_test.go`：相同用例分别运行 SQLite 和隔离 PostgreSQL schema。
- `admin/account_ops.go`、`admin/account_ops_test.go`：管理员 API、模块生命周期、轮次调度、原生请求/判题、取消与 mock 上游回归。
- `admin/handler.go`、`admin/quality_test_handler.go`、`database/quality_tests.go`、`main.go`：路由、内部文本请求、数据库启动、调度 start/stop 接线。
- `proxy/account_ops_observer*.go` 与各渠道 executor 的小型 defer 挂钩：异步告警观察，不改上游响应。
- `frontend/src/pages/{AccountQuality,AccountOps}.tsx`、`account-ops.css`、`frontend/src/lib/accountOps.ts`、`api.ts`、`App.tsx`、`Layout.tsx`、导航语言文件：两页完整管理 UI、API、路由与入口。
- `frontend/src/lib/accountOps.test.mjs`、`frontend/tests/accountOps.browser.test.mjs`：源默认值与真实浏览器 mock API 冒烟。
- `go.mod/go.sum`：五段 Cron 与移植测试使用的依赖。

## 验证

所有上游请求均使用本地 httptest / 已有原生 mock hook；浏览器 API 全拦截。没有真实模型请求或真实邮件。

```sh
GOPROXY=https://goproxy.cn go test ./accountops ./database ./admin ./proxy \
  -run 'Test(AccountOps|AccountQuality|QualityTest|QualityOutcome|JudgmentStrictJSON|Plan|SMTPConfig)' -count=1

CODEX2API_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:55491/account_ops_test?sslmode=disable' \
  GOPROXY=https://goproxy.cn go test ./database \
  -run 'TestAccountOps|TestAccountQuality' -count=1

GOPROXY=https://goproxy.cn go build ./...
git diff --check

cd frontend
npm run typecheck
npm run build
node --experimental-strip-types --test src/lib/accountOps.test.mjs tests/accountOps.browser.test.mjs
```

- Go：四个相关包全部 PASS；包括原有 QualityTest 回归、新增 SQLite 持久化、并行回答、取消不执行动作、SMTP 密码掩码、结构化信号与嵌套执行器去重。
- PostgreSQL：独立 `postgres:16-alpine` 容器、每测试独立 schema；全部相关持久化测试 PASS；完成后停止并自动移除该临时容器。
- `go build ./...`：PASS。`git diff --check`：PASS。
- 前端 typecheck：PASS。生产构建：PASS，2770 个模块；仅存在环境既有 Node `module.register()` deprecation warning。
- Node 测试：2 项默认值/动作标签 PASS；Chromium（本机 Chrome，隔离新上下文）浏览器 smoke PASS：默认关闭、启用、选择账号、建规则、立即检测、详情、游标加载更多、保存 SMTP/邮件配置、390px 移动端无横向溢出及无 pageerror。
- 浏览器需本地 Vite：`npm run dev -- --host 127.0.0.1 --port 5197`。可用 `ACCOUNT_OPS_TEST_URL` 改地址，`PLAYWRIGHT_CHANNEL` 改浏览器。该服务器完成后停止。
- 截图验证：`/tmp/account-quality-port-desktop.png`、`/tmp/account-alerts-port-mobile.png`。均为 mock 账号，不含生产信息。
- 独立复审发现的取消竞态、软删除、Gemini 入口观察、过期原生任务、Antigravity effort、Cron 本地时区均已修正并增加直接回归。

## 未验证与交付状态

- 没有连接生产/测试站，没有访问真实上游或发信；真实供应商模型效果与真实 SMTP 投递需部署后按目标环境验收。
- PostgreSQL 验证覆盖真实事务、租约、持久化、恢复、冷却及迁移初始化；没有做全系统负载或多节点 SMTP 压测。
- 只在独立分支提交；没有推送、改根工作树 main、合并、部署。原有根工作树改动保留。
