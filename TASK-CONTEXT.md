# 当前生产交接：2026-10-27

额度移植、最新上游、定时降智检测恢复及netcup生产部署完成。权威简报 docs/releases/2026-10-27-quota-upstream.md。

- 已部署defac31e25eb，tree1cd8814f，上游3c6adfda，版本release-20261027-defac31e25eb。
- 主站159.195.12.230，维护12.603秒；检测enabled/600秒保持、历史7→8，凭证worker在线。
- 当前服务基线以服务器/opt/codex2api/source-release.json为准，禁止用旧共享目录HEAD或旧镜像标签推断。
- 先读AGENTS.md和docs/collaboration.md。原共享目录51文件保留且hash不变；分支e2517414，独立任务worktree开发，发布锁和源码基线拒绝旧候选。
- 已恢复写入，禁止旧dump覆盖。恢复入口见发布简报。本次无待部署功能，文档收尾commit无需重复构建或重启。
