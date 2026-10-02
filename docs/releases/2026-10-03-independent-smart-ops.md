# Codex2API 独立智能运维发布 — 2026-10-03

- **目标与结果：** `https://codex.xingqiaolab.top`，发布成功，无回滚。API 与独立 `codex2api-credential-runtime` 均 healthy。原有 70 个账号、1 个 API Key 保留。独立测试站与旧备用服务器未查询、未同步。
- **改动：** 合并官方 `08030dd23f438e0e179afb9140ebf8d4c5d3345e`；原生添加账号 2FA 导入；自动配置、优先调度、质量运维、账号运维、凭证守护、凭证运营、鹈鹕测智；独立插件启停、来源比较工具及登录执行器单独更新脚本。两应用没有共享业务服务、数据库、凭据或 worker。
- **发布源码：** commit `2f5d30f12bb2eef86a9aac79379be66997431fdc`；tree `165d9a0654adb2f769f805389f01df319f271bde`。从已推送且干净的根目录 `main` 构建，保留原有未提交修改的独立快照与 stash。
- **应用制品：** `codex2api:release-2f5d30f12bb2`，digest `sha256:cf1714238bb1f66ff128d941dce450667fb8b06c8e8ef7d508ffeca470913b5c`。
- **执行器制品：** `codex2api-credential-runtime:release-2f5d30f12bb2`，digest `sha256:db2e3053d3e9148c3d62054225d2a48415d186f342f6f5a8b03d987a92ded39b`。锁定 toSub2 `8548397e89bf80e508eda64a87e0d556d43abc84`、Turb `d32e49e623dddf71b5fa6f0f5b0250bef963bdbd`，密钥与 worker 令牌仅在 Codex2API 专属 `0600` 文件中保存。
- **复用/新增验证：** Go 全包首轮其余包通过，四处集成夹具修正后完整管理端、数据库、smartops 重跑通过；原生质量证据测试确认执行且通过。相关 SQLite/PostgreSQL、并发与取消测试通过；前端类型检查、392 项单元测试及构建通过；Python worker 8 项测试、锁定引擎模拟登录及镜像 `--check` 通过；54 项发布测试通过。桌面/手机端 2FA 表单、实际本地排队取消与零提前建号、自动配置保存验证通过。部署阶段完成备份恢复演练、迁移、七模块接口、版本及公网前端 JS 校验。
- **耗时：** 应用构建约 141 秒、执行器构建约 231 秒（构建日志时间）；发布控制器总计 18.53 秒。恢复演练与预检 5.83 秒；连接排空 0.01 秒；停服备份 0.56 秒；迁移 1.25 秒；应用内部验证 2.28 秒；执行器就绪 6.64 秒；公网验证 1.12 秒。Codex2API 维护窗口约 11.33 秒，未强制终止残留连接。
- **Sub2API 保护：** 只更新 Codex 路由；Sub2API API、业务 worker、重登 worker、检测器、PostgreSQL、Redis、Caddy 及其他受保护容器的 ID/启动时间前后相同。Sub2API 健康验证 200；未操作其代码、数据与凭据。额外外部采样使用默认 Python UA 时，两域名均被 Cloudflare 返回 403；换用既定发布 UA 均为 200，因此不把该组被拦截的采样作为连续可用率证据。
- **回滚入口：** `/opt/codex2api/backups/20261003-2f5d30f12bb2/compose.rollback.yml`、同目录 `stopped.dump` 与 `release.json`；旧镜像保留。迁移前备份恢复已演练。恢复流量后不得直接恢复旧数据库覆盖新写入，按兼容应用回退或受控数据恢复处理。
- **边界与未验证项：** 首次真实 OpenAI 账号登录、真实邮件取码和外部 Session Studio 未使用真实凭据验收。Mihomo 与 BPS WebSocket 加速当前明确不支持并拒绝启用；原生 `ip_pool` 与标准 BPS SSE 路径已适配。Go 插件独立维护/启停，代码更新仍需宿主构建；登录执行器可独立镜像更新。
