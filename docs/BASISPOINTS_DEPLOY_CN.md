# hlool 自部署号池 · Docker 部署

由 [hlool](https://linux.do/u/hlool) 维护。**学 AI，上 L 站。**

此分支给 Codex 号池增加全局上游开关。设置路径：**系统设置 → Codex → 传输 → 启用 Basispoints（实验）**。开关默认关闭，保存后立即生效；开启后，符合模型名单、Key/分组权限、账号路径配置和能力条件的请求可以使用 Basispoints。其他渠道账号继续使用各自的接口。

## 只有指定模型走 Basispoints，其余走原 Codex 接口

开启 Basispoints 后，**只有实测可用的模型走 BPS 通道，其余模型逐请求改走原 Codex 接口**。原因：BPS 上游只对少数 Codex 模型放行，其它模型一律以 `basispoints_model_access_changed`（403）硬拒——生产日志里这类 403 曾占相当比例。默认放行 `gpt-5.6-sol` 和 `gpt-6-astra`。

- 环境变量 `BASISPOINTS_MODELS` 覆盖白名单：逗号分隔，大小写不敏感，匹配精确名或合法 `YYYY-MM-DD` 日期快照；近似前缀和非法日期不匹配。例：`BASISPOINTS_MODELS=gpt-5.6-sol,gpt-6-astra`。
- 设为 `*` 或 `all` 扩大候选模型范围，不授予账号或 Key 额外权限，也不表示所有请求强制使用 BPS。
- 不在白名单的模型：**完全按原 Codex 通道处理**——保留 State 校验、原生图片工具注入、原生 web_search，不套用 BPS 的工具信封协议，也不打 BPS 响应头。等价于「对这个模型没开 BPS」。
- 默认策略按**每个请求的实际 model** 决定；Key 可以进一步限制为原 Codex、BPS 或设置优先路径。能力证据按账号、上游和精确模型分别记录。

## 双上游策略与受控回退

配置优先级为 Key 的 `limits.codex_route_policy` → `CODEX_ROUTE_POLICY` → 原模型默认规则。未配置的新字段兼容旧 Key；全局 BPS 总开关始终是硬限制。`codex_only` 和 `basispoints_only` 不跨上游；`basispoints_prefer` 的原 Codex 备用路径还需 `BASISPOINTS_NATIVE_FALLBACK=true`。账号仍使用同一 ID、凭据、额度和并发。

当前 Compose 文件转发 `CODEX_ROUTE_POLICY`、`BASISPOINTS_MODELS` 和 `BASISPOINTS_NATIVE_FALLBACK`；修改 `.env` 后重新创建容器生效。只设置这些环境变量不会开启后台 BPS 总开关。

上游示例 `server_error`、`code:null`、精确消息 `403: This request was blocked by our usage policy.` 只有在来源可信、尚无输出或用量、备用路径权限与 State 满足条件时，才允许同账号切换一次。明确的内容安全拒绝、本地权限拒绝或无关的“403”文本不触发。完整策略、配置示例、数据库兼容和回滚要求见[双上游说明](codex-dual-upstream.md)。

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

镜像构建只发布镜像和离线包，不自动部署服务器。**Deploy Render** 改为仅能手动触发。离线 AMD64 包同时包含 `basispoints` 和对应 `sha-<完整提交SHA>` 标签；建议设置 `CODEX_IMAGE` 为固定 SHA 标签后自行部署。

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

## 内嵌图片自动转成自托管 HTTPS 链接（不回退）

BPS 只接受**绝对 HTTPS 图片 URL**，硬拒 `data:image/...;base64`、`file_id` 和 `image_url` 对象，也没有 `/files`、`/uploads` 之类的上传端点。因此代理在请求出站前把客户端发来的 base64 内嵌图片**自动落盘并生成签名 HTTPS 链接**，改写后照常发给 BPS，模型能真正看到图片；不再为图片回退原 Codex。

- **覆盖范围**：`input[].content[]` 里的 `input_image`（用户 / assistant 消息）和工具结果 `function_call_output` / `custom_tool_call_output` 的 `output` 数组里的 `input_image`（例如 `view_image` 返回的 base64）。多轮续链时历史里的图片也会在每次出站前重新转换；同一请求内相同字节只落盘一次，同一进程 12 小时内相同图片复用已有资产。原本带 `file_id` 的条目改写后去掉 `file_id`，`detail`（`auto`/`low`/`high`）保留。
- **复用仓库自带图床**：存储走系统设置里选的图片后端（`internal/imagestore`，本地目录或 S3），元数据写 `image_assets`（`job_id=0`，`model` 固定为 `bps-inbound` 以便与生图工作室的生成图区分），链接由 `internal/signedasset` 签名，形如 `<IMAGE_ASSET_PUBLIC_BASE_URL>/p/img/<id>?exp=...&sig=...`，由 `/p/img/:id` 只验签不鉴权地对外提供。**不接第三方图床。**
- **前提（缺一不可，否则功能不成立）**：
  1. `IMAGE_ASSET_PUBLIC_BASE_URL` 必须是**公网可访问的 HTTPS 域名**，而且 **OpenAI 的服务器能拉到**（BPS 是服务器端去取图，不是浏览器）。生产服若只有裸 IP + HTTP 端口，需先套一个 HTTPS 反代/域名并把 `/p/img/` 转发到 codex2api。未配置或不是 `https://` 时启动日志会打印 `image host disabled`，图片请求继续按下面的回退规则处理。
  2. `IMAGE_ASSET_SIGNING_SECRET` 必须是固定值，否则每次重启换随机密钥，多副本互不认、链接失效。
  3. 图片存储可写：本地目录（`IMAGE_ASSET_DIR`，默认 `/data/images`，Docker 部署务必持久化 `/data`）或 S3 凭据齐全。
- **校验与上限**：只接受 base64 编码的 `image/*` data URL，按字节嗅探确认是真实图片（PNG/JPEG/GIF/WebP/BMP），单张解码后不超过 10MB；不是图片、超限或无法解码时按 `image_input` 拒绝（或回退）。代理不会去拉取任意外部 URL，没有 SSRF 面。
- **隐私 / 保留**：图片托管在带签名、会过期的公网 URL 上，任何拿到链接的人（含 OpenAI）都能在有效期内取到。签名链接有效期 6 小时（覆盖单次请求的慢速拉取与重试），入站图片保留 24 小时后由后台每小时一次的清理任务删除（数据库行 + 存储对象），不会无限堆积。图片字节、base64、data URL 和生成的链接都不会写进日志，日志只记录 `stage=images result=hosted converted=N reused=M`。
- **降级**：图床不可用（未注册 / 基址不是 HTTPS / 落盘或写库失败）时，`BASISPOINTS_NATIVE_FALLBACK` 开着就回退原 Codex（响应头 `X-Codex2API-Basispoints-Bypass: image_input`），关掉则本地返回中文 400，`category=image_hosting`，提示检查上述三项前提。转换成功的请求**不会**回退。

## 自动回退到原 Codex 通道（搜索 / 结构化输出 / 强制工具）

开启 Basispoints 后，BPS **技术上无法承载**的请求会**逐个请求自动改走原 Codex 通道**，而不是本地报错，功能保持可用。这几类都是 BPS 上游硬拒（422）或行为不符，无法在 BPS 通道内实现，回退是唯一可行解：

- **联网搜索**：客户端**显式**要求联网（`web_search` 工具带 `external_web_access:true`，或 `indexed`/`live` 模式，或 `tool_choice` 指向 `web_search`）时回退到原 Codex，用原本的联网搜索能力。BPS 硬拒托管 `web_search` 声明，它自己的原生搜索跑在 Excel 沙箱里，不是客户端要的联网链路。Codex CLI 默认的 `cached` 声明（`external_web_access:false`）不含联网意图，仍留在 Basispoints，并在提示词里告诉模型如何开启（`--search` 或 `web_search = "live"`）。Codex CLI 的 standalone `/alpha/search` 端点本就直连 ChatGPT 后端，不受此开关影响。
- **结构化输出**：`text.format` / `response_format` 为 `json_schema`/`json_object` 时回退到原 Codex（BPS 返回 422）。
- **强制指定工具**：`tool_choice` 强制某个具体工具时回退到原 Codex（BPS 只认 `auto`/`none`）。
- **托管生图**：`image_generation` 工具或其 `tool_choice` 回退。
- **无法转换的图片**：`file_id`、`http://` 链接等代理无法托管的图片形式回退；base64 内嵌图片按上一节转换，不回退。

回退请求在响应头标记实际上游和固定原因。原 Codex 备用路径必须重新验证账号和精确模型的 State，不能继承 BPS 的 State 绕过。每次尝试从原始请求构造，不转发 BPS 工具信封；带 `previous_response_id`、加密 reasoning、compact 引用或不完整工具历史的请求不能直接跨路径重放，需提供完整、规范化的明文历史，否则返回明确续链错误。已有输出或用量时不切换。

环境变量 `BASISPOINTS_NATIVE_FALLBACK=off` 从 BPS 优先策略中移除原 Codex 备用路径，包括图片和协议降级。它不改变模型名单，也不覆盖显式 `codex_only` / `codex_prefer` 策略。不满足路径或协议条件时明确报错，不静默绕过限制。默认开启。

## 子代理明文消息兼容

已参考 [sub2api PR #78](https://github.com/ranxi2001/sub2api/pull/78) 实现工具参数标记修复：BPS 工具桥生成的函数调用显式携带 `encrypted_function_args: []`，让 Codex 将 `collaboration.spawn_agent`、`send_message` 和 `followup_task` 的明文 `message` 作为子代理 `input_text` 处理，避免误包装为 `encrypted_content`。

标记在 SSE 的 added、done 和 completed 阶段保持一致，并通过 HTTP JSON、SSE 和 WebSocket 入口验证。直接调用已声明工具时，保留上游已有的非 null 加密参数声明。OfficeJS 外层信封的加密字段不会错误传给客户端工具；custom 工具协议不变。

更新后请用新会话验证。旧会话中已经错误保存的 `agent_message.content[].encrypted_content` 不会自动转回明文；未知密文仍然拒绝，不根据内容猜测或丢弃。回归使用模拟上游覆盖三种协作调用、独立子会话和缓存命中／丢失后的历史恢复，不代表真实 CLI 或生产账号验收。

## 工具协议失败的修复（不回退）

模型不按 `run_officejs` 信封约定输出曾经整轮报 `basispoints_protocol_error`（500）。这些是可修的桥接 bug，按类别做了针对性处理，全部是**语法级恢复**，不执行任何代码，有歧义仍然失败：

| 类别 | 现象 | 处理 |
| --- | --- | --- |
| 幻觉原生工具 | 模型调用 `read_ranges`、`search_workbook` 等宿主 Excel 工具（`unsupported native tool` / `outside the client's catalog`） | 提示词明确列出这些宿主工具在本请求中不存在。若同一响应**同时带 assistant 文本且没有其他工具调用**，丢弃该幻觉调用、正常把文本发给客户端，日志记 `dropped_native_tool tools=read_ranges`（只记录小写下划线形式的原生工具名，其他一律 `redacted`）。没有文本、或旁边还有真实客户端工具调用时仍整轮失败，不会只执行一半。 |
| `code` 不是纯 JSON 信封（`format=text_or_code`） | 模型把信封写进赋值/`return`/OfficeJS 包装/散文里，或写成 `functions.shell({...})` 调用表达式 | 从文本中定位**唯一**一个命中目录工具名的完整 JSON 对象作为信封（重复出现的相同信封视为一个；两个不同信封判歧义失败）；`NAME({...})` 形式在 NAME 精确等于目录 function 工具且括号内是一个完整 JSON 对象时接受。纯散文、OfficeJS 代码、截断 JSON 仍失败。 |
| custom 工具忘记加 `codex2api.custom/` 标记 | `code` 是原始 patch / 脚本，`summary` 是普通描述 | `*** Begin Patch` 开头且目录有唯一 custom `apply_patch` 时直接作为其输入；否则 `summary` 里恰好提到一个已声明的 custom 工具名（精确 token）时作为该工具输入。`code` 以 `{` 或 ``` 开头（像 JSON/代码块）不走这条。 |
| `custom tools require input text, not arguments` | custom 工具用 `arguments` / `args` 对象而非 `input` 文本 | `input`/`args`/`arguments` 三者取其一：字符串原样；只有一个键且值是字符串的对象解包；多键对象按紧凑 JSON 原样交给客户端工具由其自行报错；空对象或非文本仍失败。 |
| 直接调用时类型不匹配 | custom 工具以 `function_call` 出现、function 工具以 `custom_tool_call` 出现 | 载荷符合声明类型时接受：custom 工具的 `arguments` 按上一条规则转成文本；function 工具的 `input` 必须是一个 JSON 对象。`{}`、非 JSON 文本仍失败。 |
| `completed response omitted an original tool item` | `response.completed` 的 `output` 缺少或为空，但完整的工具 item 已经在 `response.output_item.done` 到达 | 用 done 事件里的 item 补齐后正常转换，日志记 `restored_tool_items`；若 completed 里同一 `call_id` 以另一个 item id 出现，不重复下发。 |
| `missing_separator` / `unexpected_eof`（JSON 缺分隔符、未转义引号、截断） | 内层 JSON 结构损坏 | **不修**：补逗号或猜引号都会改变参数含义，违反「只补救、不猜测」原则。保留结构化诊断，依靠提示词与上面的文本兜底。 |
| `598` 首字超时 / `499` 下游取消 | 出站代理或网络层 | 不属于 BPS 协议逻辑，先按时间窗量化归因，不要误判为号池故障。 |

信封里的工具名允许带宿主显示前缀 `functions.`（与直接调用一致）。所有恢复路径都会把原始原生 item 缓存用于下一轮回放，客户端拿到的是标准 `function_call` / `custom_tool_call`。

## 使用和限制

- `max`、`ultra` 转成实际支持的 `xhigh`，响应中的 `reasoning.effort` 和用量记录显示实际档位；不会声称执行了更高档位。`none`、`minimal` 转成 `low`。
- **报错信息中文化**：本地协议转换阶段拒绝的请求（`basispoints_invalid_request`）返回中文说明加英文原文，指明是图片格式、工具历史、结构化输出还是推理设置问题及如何处理；模型不守工具信封约定的 `basispoints_protocol_error` 也返回中文说明（内置工具无法转发 / 数据流异常 / 格式不符）加英文诊断；上游 `basispoints_model_access_changed` 提示「当前模型在 Basispoints 渠道不可用，请更换模型或关闭后新建会话」。用量日志仍只记录固定 `category` 标签，不落客户端机密。
- 真实账号已验证 `gpt-5.6-sol` 的文本、`max → xhigh`、函数工具调用和工具结果回传。测试中的 `gpt-5.4` 被上游以 `basispoints_model_access_changed` 拒绝。模型名称保持原样，不会偷偷换成另一个模型。
- 此接口带有 Excel 产品的上游行为和提示词。兼容文本、HTTPS URL 图片、客户端 function/custom 工具，不能保证与原 Codex 通道的行为完全一致。未被上面「自动回退」拦下的场景里（例如 Codex 默认 `cached` 搜索声明），`auto` 下会略过已知的托管工具声明并在提示词告知模型该能力不可用，同时在 HTTP 响应头 `X-Codex2API-Basispoints-Warnings` 中列出；不会声称已经联网搜索或生成图片。未知工具类型仍明确报错。`tool_choice:none` 不处理工具目录。
- 图片可直接使用 Responses 的 `{"type":"input_image","image_url":"https://...","detail":"auto"}` 格式，`detail` 可省略或使用 `auto`、`low`、`high`，HTTPS 图片原样走 Basispoints。`data:image/...;base64,...` 由代理转成自托管签名 HTTPS 链接后走 Basispoints（见上文，需配置 `IMAGE_ASSET_PUBLIC_BASE_URL` 等前提）；本地路径和文件 ID 无法托管，默认回退原 Codex，`BASISPOINTS_NATIVE_FALLBACK=off` 时本地返回中文 400。代理自身不把用户图片上传到第三方。
- 开启后上游统一走 HTTP/SSE；客户端 WebSocket 入口仍由代理转换。Basispoints 使用独立连接池，不继承 Codex 的 HTTP/2 保活探测截止时间。网络读取错误保留为传输错误，交由现有重试逻辑处理。
- 工具目录作为 developer 消息发送，客户端 `tools` 不直接传给上游。`run_officejs.code` 中的 JSON 支持 `name/arguments` 和 `tool/args` 两种信封，并兼容对象、重复 JSON 编码、完整 JSON 代码块和短文字前缀。仅在严格解析失败时修复非法反斜杠转义及字符串内原始换行、回车和制表符，保留实际工具文本；多条信封拼接、任意 JavaScript、缺失引号或截断内容仍会报错，不会猜成空参数后执行。
- 工具目录使用自然语言描述名称、参数、必填项和约束。最多解开两层重复的 `run_officejs` JSON 包装；custom 工具支持 `input` 或字符串 `args`，原始文本保持不变。歧义字段继续报错。
- custom 工具优先使用原文传输：`run_officejs.summary` 精确等于 `codex2api.custom/完整目录工具名`，`code` 直接保存原始输入，减少一层 JSON 转义。只有已声明的 custom 工具能使用该标记；普通 function、未知工具、缺失 call_id 均不能借此绕过校验。旧 JSON 信封继续兼容。
- 普通文本实时转发；工具事件等到 `response.completed` 的完整原始 item 到齐并验证后再一次性发给客户端。不使用中间 item 的不完整参数触发工具，也不把失败或未完成响应中的原生工具交给 Codex 执行。
- 代理只转发已声明的客户端工具，不执行 Office 代码。完整原始工具 item 按账号和客户端隔离缓存，回放保留 `id`、`call_id`、`summary`、`references` 等原始内容。工具结果缺少 item ID 时生成稳定 ID；只有结果项时，从缓存补回完整原始调用。同一用户轮次保持 `turn_id`，工具结果增加时递增 `agent_iteration`。
- 原始工具 item 缓存有容量上限，保存在当前进程内。服务重启、跨实例、切换账号或缓存逐出后，只要客户端提供完整的 function/custom 调用历史，就按其原始参数重建完整 transport item。不会恢复已丢失的原生说明文字；有缓存时仍优先使用原始 item。只有工具结果、没有原调用也没有缓存时，仍需补齐历史或新建会话。
- Codex WebSocket 客户端设置 `store:false` 时，BPS 转 HTTP 通道仍保留有容量和有效期限制、按客户端隔离的本地续链上下文；向 BPS 发送的 `store` 始终是 `false`。相同工具在顶层和 `additional_tools` 中重复声明可去重，冲突定义仍报错。
- 原生 `update_plan` 仅在客户端声明了唯一兼容的同名 function 且参数满足其 schema 时转换，缓存原始调用身份。不会把失败工具结果改成成功。
- 图片转换与校验同时覆盖用户消息和工具结果，`view_image` 返回的 base64 图片同样会被转成签名 HTTPS 链接后再传给 BPS。
- 关闭开关恢复原 Codex 上游。切换前后请新建会话，避免旧上游的加密历史或压缩内容混用。
- 本机测试通过 `http://127.0.0.1:9000` 代理访问。服务器上的 `127.0.0.1` 是服务器/容器自身，不是你的电脑；若服务器需要代理，请配置它能访问到的代理地址。
- 账号权限、模型权限、额度和限流仍由上游决定。开关切换的是请求路由，不能增加账号权限。

## 常见错误怎么处理

| 错误 | 处理方式 |
| --- | --- |
| `basispoints_model_access_changed` | 当前模型在这个上游不可用。选择账号有权限的模型，或关闭 Basispoints 后新建会话。线上记录中 `gpt-6-astra`、`gpt-5.6-sol` 有成功请求，而 `gpt-6-sol`、`codex-auto-review` 曾被拒绝；不代表每个账号权限相同。代理保留明确错误，不再把它误判成整号欠费冷却、号池故障或反复换号重试。 |
| `basispoints_protocol_error` | 模型没有遵守客户端工具信封约定。常见 JSON 包装、嵌在代码/散文里的唯一信封、`NAME({...})` 调用式、漏掉标记的 custom 原文和 `arguments` 形式的 custom 输入都已兼容（见「工具协议失败的修复」）；带 assistant 文本的幻觉原生工具会被丢弃并照常返回文本。纯 OfficeJS、截断参数、歧义批量调用仍明确失败，不执行本地代码，也不因此惩罚账号。 |
| `basispoints_invalid_request · stage=prepare` / `stage=images` | 请求在本地协议转换或图片托管阶段被拒绝，尚未发给 BPS。用量日志记录固定 `category`，例如 `tool_history`、`image_input`、`image_hosting`、`tool_choice`；不会惩罚账号或换号重试。 |
| `image_hosting` | 图床不可用：`IMAGE_ASSET_PUBLIC_BASE_URL` 未配置或不是 HTTPS、`IMAGE_ASSET_SIGNING_SECRET` 未固定、图片目录不可写或 S3 失败。默认回退原 Codex（`X-Codex2API-Basispoints-Bypass: image_input`），`BASISPOINTS_NATIVE_FALLBACK=off` 时返回中文 400。启动日志出现 `image host disabled` 即表示基址不满足。 |
| `format=...; bytes=...; json_offset=...; json_failure=...` | 工具解析错误的结构诊断。`raw_control` 表示原始控制字符，`invalid_escape` 表示转义问题，`trailing_data` 表示首个 JSON 后还有内容，`unexpected_eof` 表示截断。不会记录原始工具代码、参数或提示词。只有字节数和偏移量时，不能断言具体原因。 |
| `499` / `context canceled` | 下游客户端或反向代理先取消了请求，不等于账号失效。检查 Codex 与反向代理的超时设置；若总在接近固定秒数中断，优先核对该时间限制。代理默认每 30 秒发送 SSE 保活，但不能覆盖客户端的总请求时限。 |
| `data:image/base64 image input without an image host` | 客户端以内嵌 base64 发送图片，但图床未生效，转换没有发生。检查上面的 `image_hosting` 三项前提；配好后内嵌图片会自动转成签名 HTTPS 链接走 Basispoints。 |
| `unexpected EOF` / WebSocket `1006` | 检查出站代理和网络。若多个不同模型、不同账号的流同时断开，先核对代理重启、IPv6 删除或定时轮换的时间；更换 Docker 标签或思考档位不能修复被切断的连接。 |

IPv6 轮换需要保留仍承载连接的旧地址。直接删除旧 IPv6 并重启 SOCKS 代理会打断在途请求，即使将周期调长，轮换发生时仍会断流。优先在业务空闲时轮换，或使用保留旧连接、等待其结束后再回收地址的方案。

参考：[Docker Compose 文档](https://docs.docker.com/compose/gettingstarted/)、[GitHub Container Registry 文档](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。

工具兼容处理对照了 [cpa-plugin-oai-basispoints](https://github.com/JaxsonWang/cpa-plugin-oai-basispoints) 的自然语言目录和嵌套包装处理，以及 [excel-codex-bridge](https://github.com/Kaixxrua/excel-codex-bridge) 等待完整工具 item、保留文本流式输出的方式。2026-09-25 使用线上同一账号的合成请求验证了 `gpt-6-astra` 函数工具和 Codex 风格 custom 工具的调用及结果回放；仅回传模拟结果，没有执行客户端命令。简单验证成功不代表所有长会话均已覆盖。

2026-09-25 补充对照 [ghcp_proxy](https://github.com/Nonary/ghcp_proxy) 与 `bps_client.py` 后，增加了换号、重启、缓存逐出、大 item、重复目录、HTTP/WS 两轮续链和多行代码回归。真实 BPS 合成测试返回的 7,893 字节 custom 输入与请求原文一致；使用本版本 Go 转换器在空缓存下重建历史后，BPS 返回 200 / `response.completed`。这验证了完整历史恢复路径，不代表所有生产 JSON 生成错误均已消除。

同一长度的 custom 原文标记模式也完成真实 BPS 两轮验证：首轮保留精确 summary 标记和全部输入原文，结果回放后第二轮正常完成；两轮均为 200 / `response.completed`。测试没有运行返回的客户端代码。该模式减少生成内层 JSON 的出错机会，不能保证模型永远按协议生成。
