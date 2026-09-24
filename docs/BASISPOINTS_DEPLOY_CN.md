# hlool 自部署号池 · Docker 部署

由 [hlool](https://linux.do/u/hlool) 维护。**学 AI，上 L 站。**

此分支给 Codex 号池增加全局上游开关。设置路径：**系统设置 → Codex → 传输 → 启用 Basispoints（实验）**。开关默认关闭，保存后立即生效，开启时全部 Codex OAuth 号池账号使用 `https://bps.openai.com/basispoints/api/responses`。其他渠道账号继续使用各自的接口。

## 镜像和名称

| 用途 | 填写内容 |
| --- | --- |
| Git 分支 | `main` |
| Docker 镜像 | `ghcr.io/hloolx/codex2api:basispoints` |
| 建议容器名 | `codex2api-basispoints` |
| 容器内部端口 | `8080` |
| 数据挂载目录 | `/data` |
| 数据库文件 | `/data/codex2api.db`（下方单容器方案） |

镜像包含前端和后端，支持 `linux/amd64` 和 `linux/arm64`。`main` 的 **Build Docker Image** 工作流发布 `basispoints`、`latest` 和 `main` 标签，同时发布 `sha-<完整提交SHA>` 标签供固定版本部署。只有工作流成功后，对应镜像才可使用。

## 新建单容器部署

适用于已安装 Docker Engine 和 Compose 的 Linux 服务器，使用 SQLite 和内存缓存。

```bash
mkdir -p ~/codex2api-basispoints
cd ~/codex2api-basispoints
curl -fL https://raw.githubusercontent.com/hloolx/codex2api/main/docker-compose.basispoints.yml -o compose.yaml
umask 077
printf 'ADMIN_SECRET=%s\nCODEX_PORT=8080\nBIND_HOST=0.0.0.0\n' "$(openssl rand -hex 32)" > .env
docker compose pull
docker compose up -d
docker compose ps
```

仅在新目录中生成 `.env`。已有部署请保留原来的管理密码、数据库环境变量和挂载目录。

打开 `http://服务器IP:8080/admin`。管理密码是 `.env` 中的 `ADMIN_SECRET`。在后台完成初始化、导入账号、创建客户端 API Key，然后开启 Basispoints。客户端 Base URL 填 `http://服务器IP:8080/v1`，客户端密钥填后台创建的 API Key。

若已使用反向代理，设置 `BIND_HOST=127.0.0.1`，由反向代理提供 HTTPS 访问。云服务器安全组需允许你选定的访问端口。

账号和系统设置保存在当前目录的 `data/` 中，重新创建容器不会清空它。不要把账号 JSON、`.env` 或数据库放进镜像或提交到 Git。

## 升级现有 Docker 部署

已有 PostgreSQL/Redis 部署时，沿用原 Compose 文件和 `.env`，仅修改 `codex2api` 服务的镜像：

```yaml
services:
  codex2api:
    image: ghcr.io/hloolx/codex2api:basispoints
```

然后在原部署目录运行：

```bash
docker compose pull codex2api
docker compose up -d --no-deps codex2api
docker compose logs --tail=100 codex2api
```

保留原数据库卷、数据库连接配置、缓存配置和容器端口。不要用新的 SQLite Compose 文件替换原 PostgreSQL 配置，否则会连接到另一份空数据库。升级前保留数据库备份和旧镜像标签。

## 无法拉取 GHCR 时

可以从成功的 **Build Docker Image** 工作流中下载 `codex2api-basispoints-linux-amd64` 附件。解压附件后，将其中的 `.tar.gz` 和 `SHA256SUMS` 上传到 x86-64 Linux 服务器：

```bash
sha256sum -c SHA256SUMS
docker load -i codex2api-basispoints-linux-amd64.tar.gz
docker compose up -d --pull never
```

此离线包仅用于 AMD64；ARM64 服务器直接拉取镜像。若镜像包仍为私有，使用具备 `read:packages` 权限的 GitHub token 登录 `ghcr.io` 后拉取，或在 GitHub Packages 页面将该镜像包设为 public。不要把 token 写入 Compose 文件。

也可以从源码构建：

```bash
git clone --branch main --single-branch https://github.com/hloolx/codex2api.git
cd codex2api
docker build --build-arg BUILD_VERSION=basispoints-local -t ghcr.io/hloolx/codex2api:basispoints .
```

## 使用和限制

- `max`、`ultra` 转成实际支持的 `xhigh`，响应中的 `reasoning.effort` 和用量记录显示实际档位；不会声称执行了更高档位。`none`、`minimal` 转成 `low`。
- 真实账号已验证 `gpt-5.6-sol` 的文本、`max → xhigh`、函数工具调用和工具结果回传。测试中的 `gpt-5.4` 被上游以 `basispoints_model_access_changed` 拒绝。模型名称保持原样，不会偷偷换成另一个模型。
- 此接口带有 Excel 产品的上游行为和提示词。兼容文本、HTTPS URL 图片、客户端 function/custom 工具，不能保证与原 Codex 通道的行为完全一致。托管工具、结构化输出和强制指定工具等不支持的请求会明确报错。
- 图片使用 Responses 的 `{"type":"input_image","image_url":"https://...","detail":"auto"}` 格式；URL 必须能被上游读取，`detail` 可省略或使用 `auto`、`low`、`high`。`data:image/...;base64,...`、本地路径和文件 ID 会在本地返回明确的 400 错误。代理不自动把用户图片上传到第三方；需要客户端提供 HTTPS 链接，或关闭 Basispoints 后新建会话使用原 Codex 通道。
- 开启后上游统一走 HTTP/SSE；客户端 WebSocket 入口仍由代理转换。Basispoints 使用独立连接池，不继承 Codex 的 HTTP/2 保活探测截止时间。网络读取错误保留为传输错误，交由现有重试逻辑处理。
- 工具目录作为 developer 消息发送，客户端 `tools` 不直接传给上游。`run_officejs.code` 中的 JSON 支持 `name/arguments` 和 `tool/args` 两种信封，并兼容对象、重复 JSON 编码、完整 JSON 代码块和短文字前缀。仅在严格解析失败时修复非法反斜杠转义，保留原本合法的转义含义；多条信封拼接、任意 JavaScript 和无法确定的参数仍会报错，不会猜成空参数后执行。
- 工具目录使用自然语言描述名称、参数、必填项和约束。最多解开两层重复的 `run_officejs` JSON 包装；custom 工具支持 `input` 或字符串 `args`，原始文本保持不变。歧义字段继续报错。
- 普通文本实时转发；工具事件等到 `response.completed` 的完整原始 item 到齐并验证后再一次性发给客户端。不使用中间 item 的不完整参数触发工具，也不把失败或未完成响应中的原生工具交给 Codex 执行。
- 代理只转发已声明的客户端工具，不执行 Office 代码。完整原始工具 item 按账号和客户端隔离缓存，回放保留 `id`、`call_id`、`summary`、`references` 等原始内容。工具结果缺少 item ID 时生成稳定 ID；只有结果项时，从缓存补回完整原始调用。同一用户轮次保持 `turn_id`，工具结果增加时递增 `agent_iteration`。
- 原始工具 item 缓存有容量上限，保存在当前进程内。服务重启、跨实例或切换账号后若缓存缺失，会明确要求新建会话，不会拼造不完整的工具历史。多实例部署需保持会话粘性。
- 关闭开关恢复原 Codex 上游。切换前后请新建会话，避免旧上游的加密历史或压缩内容混用。
- 本机测试通过 `http://127.0.0.1:9000` 代理访问。服务器上的 `127.0.0.1` 是服务器/容器自身，不是你的电脑；若服务器需要代理，请配置它能访问到的代理地址。
- 账号权限、模型权限、额度和限流仍由上游决定。开关切换的是请求路由，不能增加账号权限。

## 常见错误怎么处理

| 错误 | 处理方式 |
| --- | --- |
| `basispoints_model_access_changed` | 当前模型在这个上游不可用。选择账号有权限的模型，或关闭 Basispoints 后新建会话。线上记录中 `gpt-6-astra`、`gpt-5.6-sol` 有成功请求，而 `gpt-6-sol`、`codex-auto-review` 曾被拒绝；不代表每个账号权限相同。代理保留明确错误，不再把它误判成整号欠费冷却、号池故障或反复换号重试。 |
| `basispoints_protocol_error` | 模型没有遵守客户端工具信封约定。常见 JSON 包装已兼容；真正的 OfficeJS、未知工具或不完整参数会明确失败，不执行本地代码，也不因此惩罚账号。 |
| `format=...; bytes=...; json_offset=...` | 工具解析错误的结构诊断，用于区分空字段、JSON 字符串、数组、代码块或代码文本。日志不包含原始工具代码、参数或提示词。请保留这些字段反馈，不能只根据统一错误名称判断根因。 |
| `499` / `context canceled` | 下游客户端或反向代理先取消了请求，不等于账号失效。检查 Codex 与反向代理的超时设置；若总在接近固定秒数中断，优先核对该时间限制。代理默认每 30 秒发送 SSE 保活，但不能覆盖客户端的总请求时限。 |
| `data:image/base64 image input` | 当前图片由客户端以内嵌 base64 发送。BPS 要求 HTTPS 图片链接；改用上游可访问的 HTTPS URL，或关闭开关后新建会话。仅切换 stream 或 effort 不会解决图片格式问题。 |
| `unexpected EOF` / WebSocket `1006` | 检查出站代理和网络。若多个不同模型、不同账号的流同时断开，先核对代理重启、IPv6 删除或定时轮换的时间；更换 Docker 标签或思考档位不能修复被切断的连接。 |

IPv6 轮换需要保留仍承载连接的旧地址。直接删除旧 IPv6 并重启 SOCKS 代理会打断在途请求，即使将周期调长，轮换发生时仍会断流。优先在业务空闲时轮换，或使用保留旧连接、等待其结束后再回收地址的方案。

参考：[Docker Compose 文档](https://docs.docker.com/compose/gettingstarted/)、[GitHub Container Registry 文档](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

工具兼容处理对照了 [cpa-plugin-oai-basispoints](https://github.com/JaxsonWang/cpa-plugin-oai-basispoints) 的自然语言目录和嵌套包装处理，以及 [excel-codex-bridge](https://github.com/Kaixxrua/excel-codex-bridge) 等待完整工具 item、保留文本流式输出的方式。2026-09-25 使用线上同一账号的合成请求验证了 `gpt-6-astra` 函数工具和 Codex 风格 custom 工具的调用及结果回放；仅回传模拟结果，没有执行客户端命令。简单验证成功不代表所有长会话均已覆盖。
