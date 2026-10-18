# 凭证运营迁移验收记录

日期：2026-10-18。开发分支：`codex/credential-operations`。本记录对应[已确认设计](superpowers/specs/2026-10-18-credential-operations-migration-design.md)；使用入口见[操作说明](credential-operations.md)。

## 功能与验证矩阵

下表的“通过”指代码、自动化测试或本地运行验证，真实上游账号实测另列。

| 已确认能力 | 实现入口 | 验证证据 |
|---|---|---|
| 首次邮箱/密码/可选长期 TOTP 登录，单个与批量导入 | `frontend/src/components/TwoFAImport.tsx`、`admin/credential_operations.go` | 共用账号添加与运营中心入口；解析器覆盖文本/CSV/JSON、100 条限制和重复输入；浏览器验证首次导入请求与清理明文 |
| 持久导入、身份去重、配置和巡检原子登记 | `database/credential_operations*.go` | 真实 SQLite 与 PostgreSQL 队列/领取/写回；重复导入、重启、取消、迟到回调、配置和出口代际测试 |
| 一键启用独立加密、密钥丢失拒绝替换 | `credentialops/encryption.go`、`crypto.go` | AES-GCM、文件权限、原子初始化、普通响应脱敏、密文存在时缺钥保护 |
| 手动/批量重登，任务历史和重试 | `frontend/src/pages/CredentialOps.tsx`、`database/credential_operations_runtime.go` | 浏览器从持久任务历史重试；Excel profile 保留；任务互斥与恢复限制测试 |
| 巡检、自动重登、暂停/恢复、规则与冷却 | `credentialops/probe.go`、`database/credential_operations_monitors.go` | 鉴权与临时故障分类、成员移除/暂停代际、默认规则、付费账号筛选测试 |
| 本地/Session Studio 引擎、邮箱 OTP、并发与能力 | `credentialops/runtime.go`、`tools/credential-runtime/` | 全局继承与显式覆盖、远端凭据披露、并发控制、能力门禁、托管子进程与独立 Worker 协议测试 |
| 账号固定出口、静态代理、直连、托管 Mihomo 池 | `database/credential_operations_routes.go`、`credentialops/mihomo.go` | 固定出口代际；Mihomo 真实本地进程夹具验证每租约固定监听器、并发容量与释放；节点密文和不可用时拒绝回退 |
| 2FA 更换、待确认恢复、显式导出与本地二维码 | `database/credential_operations_totp.go`、`openai_totp_rotation.py`、`CredentialTOTPDialog.tsx` | 本地 fsync/数据库 ACK 顺序与故障注入；PostgreSQL 强制同步提交触发器和取消锁屏障；浏览器验证仅显式导出、关闭后忽略迟到响应 |
| Codex/Excel 独立授权、刷新与 401 代际隔离 | `proxy/credential_operations_grants.go`、`auth/token.go` | BPS 独立 grant、持久 refresh 消费标记、旧 401 不伤新 grant、Codex 主授权不变、Excel-only 拒绝原生入口、Free 保留 Codex 路由 |
| 原生导航、账号快捷入口、移动端与多语言 | `frontend/src/pages/CredentialOps.tsx`、`Accounts.tsx` | 11 项浏览器交互检查、桌面/移动视觉检查、三语言、原生组件与主题 |
| 退役迁移兼容、重复启动 | `database/postgres.go`、`credential_operations_lifecycle.go` | 新表族在旧模块退役后创建；SQLite 退役夹具与真实 PostgreSQL 退役/生命周期通过 |

## 已执行验证

- 前端：类型检查通过，全部 428 项单元测试通过，11 项浏览器检查通过，生产构建通过。
- 后端：数据库/管理端/凭证服务整包及聚焦回归通过；BPS/Auth 路由与 Mihomo 进程验证通过。
- Worker：镜像构建内使用固定依赖执行 Python 全部 31 项、Node 全部 9 项，均通过且无跳过；此前另在固定 toSub2 源码依赖下执行全部 6 项 Excel 适配检查通过。
- PostgreSQL：隔离 PostgreSQL 18 数据库，真实首次导入、身份去重、独立 Excel grant、持久刷新标记、TOTP 阶段与取消保护通过。数据库默认 `synchronous_commit=off` 时，触发器确认候选 ACK 事务仍为 `on`。长数据迁移后的初始化超时回归与退役夹具通过。
- 镜像：实际构建 Worker 镜像，非 root 用户 UID 10001、恢复目录 0700、无网络 `--check` 等价检查、`chrome146` impersonation 可用性、源许可与清单摘要通过。
- 迁移包：从镜像导出后挂载到无 Python/Node 的 Alpine 3.21，在不同目录以非 root、禁用网络运行；Python/Node 包装器、依赖和 Worker 预检通过。

关键命令：

```sh
GOMAXPROCS=2 go test -p 1 ./database ./admin ./credentialops -count=1 -timeout=180s
GOMAXPROCS=2 go test -p 1 ./database ./credentialops ./admin -run 'CredentialOperations|CredentialOps|TestMihomo|RetireSmartOps|RetiredOperations' -count=1 -timeout=90s
python3 -m unittest discover -s tools/credential-runtime -p 'test*.py'
node --test tools/credential-runtime/test_openai_totp_journal.mjs tools/credential-runtime/test_openai_excel_password_flow.mjs
cd frontend
npm run typecheck
npm test
npm run build
```

PostgreSQL 集成测试通过 `CODEX2API_TEST_POSTGRES_DSN` 显式接入隔离数据库，每次创建并删除专属测试库；记录不包含连接密码。浏览器检查使用模拟 API 和虚构账号，不连接上游。

## 运行包来源

- Sub2API：`e46b0b00b00b37ab74d95f9d800cc6396d83f672`，只读源码迁移，未复制其真实账号或服务配置。
- toSub2：`8548397e89bf80e508eda64a87e0d556d43abc84`；Turb：`d32e49e623dddf71b5fa6f0f5b0250bef963bdbd`。
- 运行包携带 `SOURCE-LICENSE`、`SOURCE-MANIFEST.json`、`dependencies.json`、依赖许可证及逐文件 SHA256 `manifest.json`。
- Docker Hub 认证端点不可达，本次验证使用 Public ECR 官方镜像镜像源中的相同版本 Python 3.12.12 与 Node 22.22.2；验证 Dockerfile 仅替换基础镜像仓库及 Debian HTTP 为 HTTPS。构建使用 `--network=host` 解决本机 BuildKit 桥接网络访问问题。仓库正式 Dockerfile 保留官方版本引用。
- Python 基础镜像摘要：`sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c`。
- Node 基础镜像摘要：`sha256:9f6d5975c7dca860947d3915877f85607946403fc55349f39b4bc3688448bb6e`。
- 验证 Worker 镜像：`codex2api-credential-runtime:credential-operations-validation`，摘要 `sha256:820df5e1104d7011419bb3f082872cbbfa68913ea7352b12ea258971995e3ef3`。本次运行包为 Linux ARM64；其他架构需要各自构建。

## 真实上游验收与发布边界

真实首次登录、重登、2FA 更换/恢复、Session Studio 服务及 Excel 授权获取均未实测。需要隔离测试账号和明确服务配置逐项验收；自动化协议夹具和离线启动不等价于真实账号验收。没有操作 Sub2API 在用账号，没有部署生产、推送或合并分支。

Mihomo 验证使用本地进程夹具，未下载真实代理节点或测量节点可用性。独立 Worker 使用应用宿主的回环 Mihomo 监听器时必须共享网络命名空间。数据库、运营加密密钥、Worker token 和完整恢复日志及其独立密钥需共同备份。

## 适配决策

- 使用一个后端实施任务，前端先按明确 API 合同开发，随后集成原生授权与出口。共享接口变更集中协调；接口判断错误会造成适配返工。
- 账号引擎增加“跟随全局”；切换全局影响后续领取的继承任务，显式覆盖保留。邮箱 OTP、Excel 和 TOTP 更换走本地 Worker。需要固定引擎的账号应使用显式覆盖。
- 未登记登录配置的历史账号保持原有 BPS 兼容行为；已登记账号使用独立 Excel grant。Excel 401 仅失效所消费代际。兼容策略变化需要另行迁移历史账号。
- 自动 Excel 获取要求已识别的付费母账号；未知套餐不会自动获取。这比来源中只排除明确 Free 的判断更严格，未知套餐需先补全身份/套餐资料。
- Mihomo 使用独立进程和节点租约，单次进程期限 30 分钟；独立 Worker 需共享网络命名空间。代价是每租约进程开销及超长操作终止。
- 本次构建使用相同官方版本的 ECR 镜像来源与 host 构建网络；未改变依赖版本。正式环境可继续使用原 Dockerfile，其他架构需要单独构建验证。
