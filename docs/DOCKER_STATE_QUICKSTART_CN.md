# Docker 部署与 State 跨主机复用

本仓库的镜像名称是 **`ghcr.io/hloolx/codex2api:latest`**，支持 `linux/amd64` 和 `linux/arm64`。包含「State 管理」、多账号多模型采集、复制迁移包、粘贴导入和目标主机复验。

## 最简单的部署方式

在安装了 Docker 的目标主机执行，先将 `CHANGE_ME_TO_A_LONG_PASSWORD` 改成自己的管理员密码：

```bash
docker run -d --name codex2api --restart unless-stopped \
  -p 8080:8080 \
  -e ADMIN_SECRET='CHANGE_ME_TO_A_LONG_PASSWORD' \
  -e DATABASE_DRIVER=sqlite -e DATABASE_PATH=/data/codex2api.db \
  -e CACHE_DRIVER=memory -e TZ=Asia/Shanghai \
  -v codex2api-data:/data \
  ghcr.io/hloolx/codex2api:latest
```

打开 `http://目标主机IP:8080/admin/`，使用上面设置的密码登录。此命令会对外开放 8080 端口；公网使用建议通过 HTTPS 反向代理。只允许本机或反向代理访问时，把端口映射改为 `-p 127.0.0.1:8080:8080`。

部署面板中填写：

| 项目 | 值 |
| --- | --- |
| 镜像 | `ghcr.io/hloolx/codex2api:latest` |
| 容器名称 | `codex2api`，也可自定义 |
| 容器端口 | `8080` |
| 持久化目录 | `/data` |
| `DATABASE_DRIVER` | `sqlite` |
| `DATABASE_PATH` | `/data/codex2api.db` |
| `CACHE_DRIVER` | `memory` |
| `ADMIN_SECRET` | 自己设置的管理员密码 |

账号、代理、State 和任务记录保存在 `/data` 中。更新或重建容器时复用原数据卷；不要删除数据卷。

## 用 Compose 部署和更新

```bash
curl -fL https://raw.githubusercontent.com/hloolx/codex2api/main/compose.state.yml -o compose.state.yml
export ADMIN_SECRET='CHANGE_ME_TO_A_LONG_PASSWORD'
export BIND_HOST=0.0.0.0
docker compose -f compose.state.yml up -d
```

更新只需再次设置同样的环境变量，然后执行：

```bash
docker compose -f compose.state.yml pull
docker compose -f compose.state.yml up -d
```

也可以将变量设置在部署面板或 `.env`。Compose 默认只绑定 `127.0.0.1`，上面的 `BIND_HOST=0.0.0.0` 用于直接通过服务器 IP 访问。不要把两种部署方式同时用于同一端口。

需要锁定版本时使用发布记录中的 `ghcr.io/hloolx/codex2api:sha-完整提交哈希`，或设置 Compose 的 `CODEX_IMAGE` 为该值。`latest` 随本仓库下一次 Docker 发布更新。

## 三步复制到其他主机

1. **目标机器准备账号。** 在目标机器的「账号管理」导入源机器所用的同一个上游账号成员和工作区，并配置目标机器可用的日常代理。两台机器的本地账号编号可以不同。
2. **源机器复制。** 打开「State 管理」，在未过期条目中点击「复制有效 N 项」；只复制部分模型时先勾选，点击「复制所选」。没有有效条目时，先选账号及 Sol、Terra、Luna、Astra，点击「批量获取」，等待捕获和复验通过。
3. **目标机器粘贴。** 打开「State 管理 → 导入 State」，保留迁移包模式，粘贴刚才的内容，确认自动匹配结果，点击「导入可用项」。保持「验证后自动启用」打开。任务通过后即自动复用，客户端无需手动添加 State 请求头。

源机器和目标机器都要运行包含 State 管理功能的版本。不要把 State 迁移包放到「账号导入」中，也不要把账号 OAuth 导出文件当作 State 包。

普通 HTTP 页面若不能直接访问剪贴板，会弹出只读文本框，选中并复制即可；使用 HTTPS 可直接复制。也支持下载迁移 JSON，打开文件后将内容粘贴到目标机器。迁移包含敏感 State 原值，但不含 OAuth token、管理员密码或代理密码，应通过私密渠道传递。

## 什么时候会自动复用

后续请求必须命中同一上游成员、工作区、精确模型和 `high` 推理档位。例如 Sol 对应 `gpt-5.6-sol`，Responses 请求设置 `"reasoning":{"effort":"high"}`。多账号调度时，每个被选中的账号都使用自己的 State，不会借用其他成员的 State。原生会话续接 State 仍优先保留。

迁移包会自动匹配身份和模型，在目标主机的凭据、业务出口下重新答题验证；通过后才启用。每个有效候选通常需要一次目标端复验请求，并消耗目标账号额度。已经在同一本机、相同绑定下验证过的相同 State 可直接复用本地记录。

有效期从**首次捕获起一小时**计算。复制、导入、重启或复验都不会续期；一小时只是本地策略，上游没有保证接受到期前的每次复用。勾选「到期阻止新请求」时，过期会暂停匹配请求并返回 503；需要重新采集，或关闭该条目的自动复用以恢复普通请求。当前没有自动定时补充。

**State 不会增加账号额度，也不能解除 429；通过题目不等于证明模型能力提升。** 401/403/429 会停止相关尝试，应按页面提示处理授权、出口或冷却。

## 目标机器的代理地址

容器里的 `127.0.0.1` 指容器本身。不要直接照抄源 Windows 机器上的 `http://127.0.0.1:9000`。若代理运行在目标主机宿主机，Compose 已提供 `host.docker.internal`，可以配置为 `http://host.docker.internal:9000`，但宿主机代理必须允许 Docker 网络连接。

使用 `docker run` 时，Linux 需要额外加 `--add-host=host.docker.internal:host-gateway`；Docker Desktop 通常自带该地址。也可在目标机器配置其可访问的独立代理 URL。State 迁移不会搬运源机器的代理配置。

## 发布流程

维护者在本仓库 **Actions → Build Docker Image → Run workflow** 选择目标分支，可填写版本如 `2.9.8-state.1`。工作流先启动 SQLite 单容器，验证 State API、四个模型及重启后配置持久化，再发布双架构镜像和提交哈希标签。构建成功后才能拉取新版本。

完整机制和限制见 [State 管理说明](STATE_POOL_CN.md)。
