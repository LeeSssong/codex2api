# Codex2API 协作与发布规则

## 每次任务先核对

- 用户的当前指令优先；默认主站是 netcup `159.195.12.230`。旧 `64.83.10.67` 仅历史入口，不启动旧业务；`154.13.4.147` 是冷备。服务器身份与密钥细节从受保护的本地记录读取，不打印凭据。
- 开始前执行 `git status --short`、`git branch --show-current`、`git worktree list`、`git ls-remote origin refs/heads/main`，读取本任务交接和 `docs/collaboration.md`。历史 TASK-CONTEXT 不能代替当前线上版本。
- 主共享目录 `/Users/gongtengxinwen/Documents/codex2api` 曾同时有 Grok/Codex 写入，2026-10-27 发现 51 个未提交文件。此目录在完成来源归档前是保留现场，禁止在其中开发、切分支、reset、clean、stash pop 或覆盖文件。

## 并行开发

- 一个任务一个独立 worktree 和分支（Codex 使用 `codex/<task>`；Grok 使用 `grok/<task>` 或独立 `codex/grok-<task>`），一个 worktree 只允许一个写入者。使用 Codex 原生 worktree 工具时遵循其返回路径。
- 从最新已获取的 `origin/main` 开始；领取任务时记录 owner、base SHA、路径、目标文件。共用文件如 Accounts.tsx、types.ts、locales、handler.go、store.go、main.go 由整合者按提交合并；禁止整文件复制、共享目录打包、直接覆盖容器二进制。
- 禁止 `git add .` 携带其他任务改动。提交只列本任务文件；有冲突按本次用户明确授权处理，不以静默覆盖消除冲突。2026-10-27 此轮上游冲突以 james-6-23/codex2api 为准。
- 发现其他写入者或线上版本变更，保留自己的候选，重新整合；不要求用户反复提醒。未完成或不明改动先保存在受保护快照与保留分支，不猜测作者或丢弃。

## 单一发布入口

- 同一主站只有一个发布者。发布全程持有 `/var/lock/codex2api-release.lock`；使用经过测试的 maintenance_release.py。所有脚本/人工入口必须遵守同一锁，不能绕过。
- 只部署干净、已推送且与实时 `origin/main` commit/tree 一致的源码；整合、构建期间远端 main 前进则重新合入验证。不可强推 main 或从陈旧目录构建。
- 发布前比较服务器预期 container ID、image digest、实际运行二进制 SHA256、Compose 来源。实际二进制与镜像不一致时须保全可运行回滚制品并记录来源，禁止只相信标签。
- 更新前与实际停写/切换前均复核；预期不符立即拒绝旧候选发布。只有锁不能阻止先后顺序的陈旧部署，必须同时有版本/基线比较。
- 数据库变更走停机：制品先准备，单 writer 停止后备份并验证，再迁移和启动单实例。生产恢复新写入后不得用旧 dump 覆盖；保留旧二进制/镜像/配置/路由与明确恢复步骤。
- 只操作本次目标 Codex2API；不重启 Sub2API、数据库、Redis 或冷备。凭证 runtime 的挂载、manifest、加密配置和 worker 必须保留。

## 必须保留与交接

- 定时降智检测为明确保留能力：开关、频率、调度、账号启用状态、历史、API 和页面全部验证；页面返回 HTML 不等于功能正常。已存配置不能静默关闭或重置。
- 发布完成记录 source commit/tree、upstream SHA、binary hash/image digest、数据库演练/停写备份、实际 downtime、线上验证、回滚入口和未解决项。更新 `TASK-CONTEXT.md` 与本次发布记录。
- 任务完成后区分已合并、仅保存、被上游替代的改动；旧候选标记历史，不重复发布，不删除不明 worktree/快照。自动校验失败不得靠删除检查、伪造标签或 `--force` 绕过。
