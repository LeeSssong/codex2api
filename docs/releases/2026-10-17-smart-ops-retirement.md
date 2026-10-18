# 智能运维退役发布记录

- 发布源码：`dde140b3cecbb0e9412435439dfc59d759bbddc0`，tree `ccfb3a037c22e5db3eba8b382bfd00f69c1548fa`。
- 基线：服务器 10 月 16 日整合提交 `9d187930`，保留官方与 hloolx 新增功能。
- 运行版本：`release-20261017-dde140b3`；公网 `/health` 返回 200。
- 完整移除截图智能运维七模块和插件管理，包括页面、API、后台任务、请求/调度扩展及独立凭证执行器。
- 清理 22 张专属表、5 个质量 BPS 字段和三个 smart_ops 凭据字段；重启后专属表剩余 0。
- 删除运行容器 `codex2api-credential-runtime` 及专属 `credential-app.env`、`credential-worker.env`，更新主 Compose 配置，避免重新启动旧执行器。
- 核验账号的普通凭据及并发等保留字段、API Key、分组/成员、原生路由、系统设置等迁移前后哈希一致。发布前各类所有权状态表为空；发布脚本对此再次断言。
- 公网原插件、智能运维、凭证运营 API 返回 404；设置、模型质量、IPv6、普通质量检测和 State API 返回 200。
- 本地全量 Go 测试通过；最终改动的数据库/调度/计费/路由针对性测试通过。前端类型检查、415 项单元测试、构建以及 IntelligenceCheck、ModelQualityGuard、State、Codex/BPS 桌面/移动浏览器回归通过。部署工具 55 项测试通过，PostgreSQL 旧触发器迁移测试通过。
- 发布期间非目标 `sub2api-sub2api-blue-1` 的启动时间变化，最后的保护检查因此报错；发布命令未操作该容器。只读复查该容器及 green 容器均健康，Codex PostgreSQL/Redis 未重启。该差异已记录，不能声称所有非目标容器完全未变。
- 服务器证据：`/opt/codex2api/backups/20261017-020607-retire-smart-ops-dde140b3cecb/release.json`。历史恢复备份保留，未声称对历史归档做彻底擦除。

新版使用路径见 [更新指南](2026-10-17-update-usage-guide.md)。
