# Codex2API 智能运维生产发布记录

- 验证时间：2026-09-25T23:08:14.513132+00:00（UTC；本次 release-id 按 Asia/Shanghai 日期命名）。
- 目标：新主站 64.83.10.67 / codex.xingqiaolab.top。测试站未访问、未同步；旧备用服务器未访问。
- 结果：成功，未回滚。原有 Sub 活跃 blue 实例、worker、数据库、Redis、探测器及其他受保护容器未被重建。
- 改动：对齐 hloolx 上游，原生质量守护、账号告警/SMTP、OAuth TokenGuard、统一智能运维入口及源码镜像更新检查；加入账号/分组控制版本和任务、审计、恢复所有权持久化。

## 来源与制品

- 发布 commit：5de5d7531e6a6b6dbc9fcadb88c77c74724d96f8
- 发布 tree：8a8a26b971608e6ccbe2938df839cff1c7b2b3b6
- 上游 main：b402c6110ff3eab83c4ac4e9083366c0abc3c08d（构建前实时 git ls-remote 核对）。
- 镜像：codex2api:smartops-5de5d7531e6a
- 同一本地/生产 digest：sha256:a925a4e273436df0c0f5138275155630cf2b6dca06611d48655c765146208fad
- 构建版本：smartops-20260925-5de5d7531e6a（UTC 日期）。
- 构建源为根目录非 detached、干净 main，与实时 origin/main 一致。只构建一次；本地验证同一镜像后上传，生产核对 tar SHA256、镜像 digest、commit/tree。未使用 GitHub Actions 发布。

## 复用与新增验证

- 复用各模块直接回归、三语言/390px UI 检查与浏览器交互证据；UI typecheck/build 通过，10 项 helper/翻译测试通过。
- 组合检查 accountops、auth、admin、proxy 通过。受本地 Docker 数据盘满影响的数据库检查移到本任务内存盘 fixture 后重跑通过：database 7.454s（SQLite+PostgreSQL）、tokenguard 0.898s。未改生产超时或把失败计作通过。
- 403/BPS 执行路径告警、质量隔离/恢复、同值/ABA/分组变更、凭据代际、取消/过期任务、COMMIT 失败、跨实例旧能力缓存及原子触发器重建直接回归通过。
- 21 项发布控制器/来源校验测试通过；构建制品的 9 个运维/版本接口认证返回200、未认证返回401，3个前端入口通过。
- 生产先恢复备份到临时数据库并运行同一制品迁移演练，再停止单一业务实例、备份、迁移和启动。内部/公网 JSON 接口、管理入口、TokenGuard/告警 JS 资源与运行 commit/tree/digest 已核对，Docker health 为 healthy。

## 阶段耗时

| 阶段 | 从发布控制器启动累计秒数 |
| --- | ---: |
| preflight-and-restore-rehearsal-passed | 4.2 |
| maintenance-on | 4.35 |
| old-requests-drained | 4.36 |
| app-stopped | 4.71 |
| consistent-backup-completed | 5.21 |
| migration-completed | 6.25 |
| internal-feature-smoke-passed | 8.1 |
| traffic-restored | 8.25 |
| public-feature-smoke-passed | 9.24 |

实际维护窗口约3.90秒；排空时在途请求为0，因此提前结束，无强制截断。发布控制器总耗时9.24秒；编译和传输均在维护前完成。

## 恢复入口

- 旧镜像保留：sha256:0c9eaca274963caaad775b292bc292912916003178056fb2138a49a8c79edd7a
- 旧镜像固定配置：/opt/codex2api/backups/smartops-20260926-5de5d753/compose.rollback.yml
- 停写一致性备份：/opt/codex2api/backups/smartops-20260926-5de5d753/stopped.dump
- 备份恢复与迁移演练已通过。流量恢复后若回滚兼容应用，应保留当前数据库，不能恢复旧快照丢弃新增数据。
- 机器证据：/opt/codex2api/backups/smartops-20260926-5de5d753/release.json；制品、SHA256及后验检查：/opt/codex2api/releases/smartops-20260926-5de5d753/。

## 未完成的配置映射及边界

- 旧 Sub 唯一质量规则 plan4/account395 在 Codex2API 无匹配 email、原生 account_id 或 user_id；依批准规格保留源规则，未猜测映射或复制整套账号/凭据。此项待明确目标账号后迁移。
- 原 account_ops 全局开关 true 已保留。TokenGuard 仍为 disabled：源站未提供可迁移的外部探活/重登/SMTP/Bark配置，本次不伪造配置或声称外部服务连通。
- BPS 专项只交付后续方案，不随本次自动实施。只读确认生产未配置 IMAGE_ASSET_PUBLIC_BASE_URL 和持久 IMAGE_ASSET_SIGNING_SECRET，内置 BPS 图片中转当前未启用；未发送真实图片、付费探测或外部通知。
- 用户发布前的3处本地未提交修改单独备份并暂存，发布后恢复；不进入制品。
