# BPS 上游优化生产发布记录 — 2026-09-26

- 目标：新主站 64.83.10.67 / codex.xingqiaolab.top；用户明确授权部署生产。测试站及旧备用服务器未访问、未同步。
- 结果：发布成功，服务 healthy，未回滚。单套数据库停机迁移，实际维护 4.12 秒；排空时在途请求为 0，没有强制截断。9 个受保护容器（包括 Sub green、worker、探测器、Caddy、数据库和 Redis）ID 均未改变。
- 改动：可信 BPS 403 分类及可选账号关闭策略、全局/账号模型范围、临时图片中转/配额/撤销/清理、可选缓存创建计费归一化、原生运维事件与已批准界面；另补图片签名异常日志脱敏及安全回滚。

## 来源与制品

- 应用 commit：1aaa1688236a9a08456d784cf51d8e218e9d9f39
- 应用 tree：fd1ba2d3fa58266f05e18b6c94d8e48b2634e42e
- 镜像：codex2api:release-1aaa1688236a；版本 release-20260926-1aaa1688236a
- 本地/生产同一 digest：sha256:5b25ad1e39c5b7ec7a58a084d387dfb1efc5c22196d50d67eca6b3e61f1a2462
- image.tar SHA256：153084270360583debc46d32ca7aff8724d53b3bd9c4ca430760ee715b6a8bd5
- 构建来自干净、非 detached、已推送且与实时 origin/main 一致的根 main，只构建一次。沿用已整合上游基线 b402c6110ff3eab83c4ac4e9083366c0abc3c08d；本轮核验其 ancestry，不声称再次检查到最新官方版本。没有使用 GitHub Actions。
- 后续仅增补验收脚本及文档；运维脚本已从干净、推送并核实的 main 上传（最后验收脚本 commit 374cefb191239a993513486ac7d793c23f1c1998），应用镜像未重建。

## 验证与最终配置

- 复用候选的相关 Go/SQLite/PostgreSQL/race、前端 9 项测试/typecheck/build、批准界面的浏览器验证。新增发布控制器 29 项、来源构建 4 项、验收脚本 7 项检查及图片 recovery 定向测试通过。
- Mac Colima 的本地挂载 SQLite/amd64 模拟验收未通过，不计作成功证据。改用生产机的隔离临时容器验证同一 digest：来源、鉴权、配置、图片字节/no-store/nosniff、关闭后重新开启仍不能恢复旧链接、页面及 JS 均通过；临时容器已删除。
- 生产先恢复备份至临时数据库，用同一镜像演练迁移并核对新增字段，再停写备份、迁移、启动。内部/公网健康、版本、BPS 及原运维接口通过；公网实际 Settings JS 包含新图片设置。
- 共 4 次实际 BPS 推理，日志 1682–1685，计价合计 $0.1070568。文本回显通过；补充的两步工具调用 → 随机截图读图返回唯一正确 8 位代码，通过字节核对、HTTPS 签名读取、no-store/nosniff、逐条 input/output/cache 与费用分项对账，关闭后的签名 URL 拒绝访问。
- 首次脚本默认客户端标识被 Cloudflare 入口拒绝，未进入 BPS、未计费；修正脚本标识后测试。普通单图首轮严格全文比对不匹配，原回复未保留，无法回溯为格式或 OCR 原因；后续工具截图通过不抹去该用例的验证缺口。未额外重放或切换账号/模型。
- 没有人为诱发真实 BPS 403；可信 403 终止请求/仅关闭原账号策略复用同源码测试，不能把入口 Cloudflare 403 当作上游策略验证。未发送真实 Bark/SMTP 通知，未另做 WS 专项。
- BPS 总开关恢复原值 false；图片中转配置 true，HTTPS origin 为 https://codex.xingqiaolab.top，持久签名密钥已从受保护 env 文件加载。账号 69 的模型继承、自动 403 关闭 false、缓存创建归一化 false、revision 0 均保持原值。功能已部署，BPS 总路由仍按用户原配置关闭。
- 临时 Key 2/3/4 均已撤销，无残留验收 Key；最终 epoch 6，旧链接失效。验证生成的临时图仍计入配额并由原生 30 分钟生命周期清理，未绕过账务或删除其他数据。最后状态核验：2026-09-26T07:36:29Z。

## 阶段耗时

构建约 51.85 秒，构建及上传均在维护前完成；同镜像原生隔离验收 1.56 秒。以下为发布控制器启动后的累计秒数：

| 阶段 | 秒 |
| --- | ---: |
| 备份恢复及迁移演练通过 | 5.52 |
| 开启维护 / 排空完成 | 5.69 |
| 应用停止 | 6.04 |
| 停写一致性备份完成 | 6.59 |
| 数据库迁移完成 | 7.72 |
| 内部验收通过 | 9.62 |
| 恢复流量 | 9.81 |
| 公网验收通过 | 10.82 |

## 回滚与证据

- 保留旧镜像 sha256:a925a4e273436df0c0f5138275155630cf2b6dca06611d48655c765146208fad，以及备份目录 /opt/codex2api/backups/bps-20260926-1aaa1688（固定旧镜像 compose、原 env 快照、stopped.dump、release.json）。
- 兼容应用回滚入口如下，先门控/排空，保留当前数据库，将 BPS 临时图隔离并撤销后才启动旧镜像；不得恢复旧快照丢弃恢复流量后的新增写入。迁移前停写备份恢复能力已演练。隔离或校验失败则维持维护，不向旧版暴露临时图。

    python3 /opt/codex2api/releases/bps-20260926-1aaa1688/release_host.py --rollback-existing /opt/codex2api/backups/bps-20260926-1aaa1688

- 机器证据：/opt/codex2api/releases/bps-20260926-1aaa1688/ 下的 manifest.json、image-smoke/result.json、post-deployment.json、bps-live-acceptance*.json、bps-tool-image-acceptance.json、final-state.json。报告不含 Key、签名 URL、图片或原始 SSE。
- 发布前用户的三处本地修改单独备份与 stash；不进入制品，发布归档后恢复并验证原补丁，保留备份及 stash。
