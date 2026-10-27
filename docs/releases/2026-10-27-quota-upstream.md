# 2026-10-27 额度移植、最新上游及定时检测恢复：已发布

netcup159.195.12.230的Codex2API发布成功，未回滚。北京时间13:43:44.093进入维护，13:43:56.696恢复流量，维护12.603秒；13:45:55完成公网冒烟及源码基线记录。

## 用户可见结果

- Codex列表/详情统一展示5h/7d已用USD、预计总额、百分比、重置、请求/Token及费用来源/观测时间；月窗30d和Spark独立。
- 沿用Sub2API的账号费用×100/官方百分比；低样本提示、缺失/失败/零值分别表达。官方观测超过10分钟保留当前费用并暂停预测。
- 最新上游main3c6adfda全部合入；冲突选上游，保留兼容BPS/State/Grok、恢复来源的凭证运营/固定出口和runtime-newyork挂载。
- 定时降智检测API、页面及周期runner恢复；生产enabled=true、interval600s、revision7保持，历史7→8，确认后台自动执行。

## 来源与制品

- 已发布commit defac31e25eb28dbeed1193ca4cc5b35c772c22c；tree1cd8814f0fef4cf58b537a897dee29a524d35f49。
- 上游3c6adfda702efed67931b2d98a491d72bd9d91b5；版本release-20261027-defac31e25eb。
- 运行AMD64镜像ID/config digest sha256:30529879d66767882532a31949b1c859f138706f0ca4ae187a4e01cc38b611ea。
- 构建OCI index digest sha256:80d254ea6f466c05cff96e41d3b3cc05f3e1f16aeb2491546c60a0190f0939f0。两层digest分别记录，不伪装一致。
- binary SHA256 4a0fd02b703033da533e5c84cf8a24caa4b7aa0a50d67081bcbd52becc812ec7。
- 旧线上为可写层替换程序，原始源码祖先未证实；完整原51文件及恢复来源分开归档，首次基线明确legacy-unversioned；实际旧二进制已保存回滚镜像。

## 验证

- 最终typecheck、构建及459前端单测通过；额度桌面/手机/明暗/列表详情和重置竞态浏览器验证通过；降智页面9项浏览器验证通过。
- 合并后全套Go中管理端175.361s、代理75.828s、WS19.304s及其余包通过；仅凭证runtime标记测试在并行负载下偶发3秒超时。该包独立重复5次通过6.844s、最终定向通过。未将该次全套称作全绿。
- 最终额度/数据库/周期runner/遥测夹具/凭证runtime差异测试通过；最终发布工具84项通过。周期集成测试自动完成两轮而非手动enqueue。
- 生产1个Codex账号的两个真实窗口均ready且有费用；缺失账号明确unavailable。真实worker API/文件心跳在线。
- 三项实时基线、隔离DB恢复迁移、停写备份、配置/APIKey/分组/账号检测标记指纹均通过。所有受保护非Codex容器ID、启动时间和配置一致。

## 生产证据与恢复

- 制品和脚本 /opt/codex2api/releases/quota-upstream-20261027-defac31e/。
- 发布/备份 /opt/codex2api/backups/20261027-quota-defac31e-production/，含stopped.dump、compose.rollback.json、container.original.json、caddy.original.json和release.json。
- 精确旧程序回滚镜像 sha256:441c4feac6460b1e14703dd567004cee9deea34fa9231dec022cb22b58f0d14d。
- 已产生新写入，禁止旧dump回灌。worker启动后的失败须保留最新DB，审查兼容恢复或修复前进；脚本强制拒绝旧快照覆盖。
- 权威 /opt/codex2api/source-release.json，后续候选须提供baseline_revision及本地祖先证明，同时持/var/lock/codex2api-release.lock并检查container/image/实际binary。

## 协作收尾

- 原共享51文件hash全部不变，完整保留codex/preserved-shared-20261027/e2517414及受保护快照。旧身份/并发冲突选上游，原文未丢弃。
- 根AGENTS.md、docs/collaboration.md和共享Git hooks生效：共享目录禁止commit/push，独立worktree开发，功能分支不能直接覆盖main。
- 干净发布checkout在本机.config/codex2api/quota-upstream-20261027/release-source，main已推送；原共享main是保留现场，不能再构建部署。
- 本次未主动触发生产登录/TOTP轮转或额外推理，不把只读冒烟当成这些业务验证。未部署Sub2API、旧站或冷备。
