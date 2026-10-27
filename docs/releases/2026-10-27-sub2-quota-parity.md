# Sub2API额度口径对齐：生产部署

2026-10-27用户追加授权立即部署，Codex2API当前无对外业务流量。目标netcup159.195.12.230。应用已更新，无回滚。

- 发布commit9aa624fb088a425668004c45f1d2fa4868a49ee0；tree a0fd74fef931910d9aebcf88a6165a42cb888f67；已合入并推送origin/main。
- 应用版本release-20261027-9aa624fb088a；上游3c6adfda702efed67931b2d98a491d72bd9d91b5。
- 实际AMD64镜像ID/config digest sha256:2211aec20046e67162fa674fdecfb456545c97da4ceea26c433c0f124c722d1b。
- 构建OCI index digest sha256:992d2302b813ec28639b7e30f50c90d95fb249e31cc24939181c44e74a8b1668。
- 实际binary SHA256 7d191c0a7fbec59498ecce290d1d8d7f49c09b0c95cd4c5e55ac5b34a51daf65。
- 北京时间19:21:26.269进入维护，19:21:44.296恢复入口，18.027秒；19:23:52公网验证通过并保存source-release记录。

## 生效功能

Sub2账号费用快照/规则、原始用量准入、精简费用账本及新旧记录统计合并已上线。普通Codex Token用户计费用量和账号成本共用最终费用；媒体按次定价独立。5h/7d窗口回退与预测、缓存5分钟、变化/手动触发静默刷新，旧值保留至成功响应替换，无10分钟预测隐藏门槛。

数据库为加法迁移：usage_logs六个可空金额/准入相关字段及来源/价格快照文本，account_usage_cost_events表。此前直接纠正的历史account_billed/user_billed原值保留。上线后检查10,688条日志账号及用户费用合计均443.73993032美元，不同金额行0；APIKey已用435.50964552，上限0。后续真实请求按新规则记账；未为冒烟主动发起新模型推理。

## 验证

- 复用本轮有效证据：数据库/代理最终完整回归通过（8.863s/62.552s）；此前管理端/WS回归、费用race、真实隔离PostgreSQL、458前端测试/typecheck/build及浏览器刷新状态回归通过。
- 本次发布工具85项检查通过；来自干净已推送main的前后端及LinuxAMD64制品构建成功。
- 发布前旧container/image/实际binary与source-release基线匹配；克隆生产备份的隔离数据库迁移及配置指纹通过；停写备份验证、单实例迁移、应用版本及二进制、额度API、凭证worker均通过。
- 生产5h/7d统计接口可显示历史账号费用、用户费用及预计总额；成本和用户费用一致。相关文档中的同价范围为普通Codex Token，不扩展至媒体单独价或非Codex供应商。
- 定时检测保留发布前的当前配置：enabled=false，interval600秒，revision8；历史19条完整。不得按旧文档enabled=true擅自重启检测。
- 凭证worker在线。所有非Codex受保护容器ID、启动时间及配置一致，Sub2API/PG/Redis未重启。

## 证据和恢复

- 生产制品 /opt/codex2api/releases/sub2-quota-parity-20261027-9aa624fb/。
- 发布证据/停写备份 /opt/codex2api/backups/sub2-quota-parity-20261027-9aa624fb-production/。
- 精确旧程序回滚镜像 sha256:3c6b643d00ad80b89d5475533c3d0659a536c50453a2630bdb32c6001c3caf7e；compose.rollback.json与旧路由保留。
- 权威 /opt/codex2api/source-release.json 已更新至9aa624fb。应用恢复后禁止用停写dump覆盖最新数据；失败恢复要保全新写入。
- 普通费用账本沿用提交前内存缓冲，只有事务提交后持久；不声称进程崩溃或缓冲超限零丢失。历史原始参数缺失仍采用已纠正的原值，未再次批量重算。
- 原共享目录51个文件保持，任务各worktree明确提交整合。文档收尾commit不构成新应用制品，不重复部署。
