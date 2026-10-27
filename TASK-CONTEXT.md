# 当前生产交接：2026-10-27 Sub2额度口径对齐已部署

用户已从“本地不部署”追加授权立即部署。权威发布记录 docs/releases/2026-10-27-sub2-quota-parity.md。

- 主站159.195.12.230；应用commit9aa624fb088a/tree a0fd74fe；版本release-20261027-9aa624fb088a，上游3c6adfda。
- 维护18.027秒，19:21:44恢复入口，公网/worker/额度API检查通过。
- 历史成本与用户计费用量纠正值保留；新普通Codex Token成本/用户价统一，媒体按次单独。
- 静默刷新与费用快照/账本上线；schema加法迁移已完成。没有待部署应用功能。
- 定时检测保留用户当前关闭状态false，频率600秒/revision8，历史19条；不按旧记录开启。
- 生产基线 /opt/codex2api/source-release.json。恢复要保全上线后的新写入，不可回灌旧dump。
- 协作先读AGENTS.md和docs/collaboration.md；共享旧目录仍是保留现场，原51文件不覆盖。文档收尾不重复构建发布。
