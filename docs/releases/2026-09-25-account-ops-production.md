# Codex2API 账号运维发布记录

- 日期：2026-09-25；目标：主站 `64.83.10.67` / `codex.xingqiaolab.top`，仅更新 Codex2API。
- 改动：归档降智运维与余额/周额度邮件告警；保留 Basispoints 及现有主线功能。新增 5 张 AccountOps 表和 `idx_account_quality_rounds`；Turn-State 两列已在线上存在，未新增其他迁移范围。
- 应用来源：干净且已推送的根 `main`，commit `fbbe7912d21bf74a6ce5a90928374d651c5b4528`，tree `f4982217428251fcd717f3894f0be01c638135cf`。构建上下文使用该 commit 的 `git archive`，没有纳入本地 env、凭据、未提交文件或运行镜像代码。
- 镜像：`codex2api:release-fbbe7912d21b`，linux/amd64；运行制品 digest `sha256:510229c121c69b2e154f9b626333d5e6f1e62cdab17ab4fd5684e1b074ed8ef0`；导出包 SHA256 `86f223a8d9c09479851808a1900891497ed800ad9ca3a353881992308f788a90`。版本标记 `3.0.0-accountops.fbbe7912`。
- 发布控制器来源：已推送干净根 `main` commit `8675f3d157849fec3e7685f0e28e730b37c853e4`，tree `1c5c6bb63c064a2ae3032343872417075e03f01f`。其相对应用来源只修改部署控制器及测试，应用源码/依赖/Dockerfile均未变化，复用同一已构建制品；未重复构建。
- 验证：复用功能专项、SQLite/PostgreSQL和模拟浏览器证据；因整合生产 Basispoints 与 main 有冲突，补测 AccountOps/Basispoints/Turn-State 等相关 Go 包、前端13项测试、类型检查与构建，均通过。发布控制器11项测试通过，独立审查通过。
- 路径：停机单套迁移。先在线备份并在同机临时数据库验证恢复，随后仅 Codex 路由维护＋阻止 Codex 子网18080新连接，停止应用、备份、migrate-only、启动、验证、恢复流量。数据库/Redis/Sub容器未重建或重启。迁移容器在失败回滚前必须清除；新应用启动后只允许应用回滚，不回放旧库丢弃新增写入。
- 阶段耗时：构建 npm ci 6.5s、前端2.2s、Go9.1s、镜像导出1.3s。成功发布预检/恢复演练2.52s，停服0.34s，停服后备份0.50s，迁移1.01s，内部验证/启动1.56s，恢复流量0.16s，公网验证0.12s；成功维护窗口3.57s，宿主流程总6.35s。整合、上传未单独计时。
- 异常与回滚：首次构建取外部 Dockerfile frontend 超时，无制品产生；改用内置 BuildKit frontend，并从官方 ECR Docker Library 补齐相同版本 Alpine。首次发布内部验证通过，公网 Python 默认 UA 被 Cloudflare 返回403，自动应用回滚完成（总9.40s，未恢复数据库）。旧版本同 UA 也返回403；明确 `Codex2API-ReleaseCheck/1.0` 后公网健康/管理员读取200，将该检查前移后复用原镜像重试成功。
- 最终结果：容器 healthy，运行 digest/源码标签匹配；公网健康、模块配置、计划/历史、告警读取均200；模块 enabled=false，计划及告警为空；Basispoints 设置保持原值 false。维护路由与临时防火墙规则已解除。
- 恢复入口：服务器 `/opt/codex2api/backups/20260925-accountops-fbbe7912-retry1/` 内受保护 `compose.before.yml`、`stopped.dump`、`release.json`；旧镜像 `codex2api:3.0.0-basispoints` 保留。原次发布目录也保留事件证据。恢复流量后禁止直接恢复旧数据库覆盖新数据。
- 未验证/未操作：未主动运行真实模型题目、未配置或发送真实邮件；需要管理员按实际账号与 SMTP 配置使用。Sub 未更新；独立测试站未查询、未同步。
- 本地已有工作：发布前3处未提交修改保存在 stash `78b2046f70fdae03d2fc5af2fe4a6ca68a3aa4cc`，原main保留分支 `codex/preserved-pre-accountops-main`；发布后恢复，stash留存不删除。
