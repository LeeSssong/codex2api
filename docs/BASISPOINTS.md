# Basispoints 上游

可选通道，默认关闭。入口在管理后台 **系统设置 → Codex → 传输 → 启用 Basispoints（实验）**。保存后立即生效，只影响 Codex OAuth 号池；其他渠道仍走各自接口。

开启后，这些账号的请求改走 `https://bps.openai.com/basispoints/api/responses`，使用 HTTP/SSE，并优先于 Codex WebSocket 设置。`max` 和 `ultra` 按 `xhigh` 发送，用量记录里的推理档位是实际发出的档位。关闭后恢复原来的 Codex 通道。切换后建议新建会话。

## 自动回退

`BASISPOINTS_NATIVE_FALLBACK` 默认开启。下面这些请求会按次回到原来的 Codex 通道，响应头带 `X-Codex2API-Basispoints-Bypass`：

- 显式联网搜索
- `json_schema` / `json_object` 结构化输出
- 强制指定某个工具
- 托管生图
- 无法托管的图片

设为 `off` 后不再回退。搜索、结构化输出和强制工具会直接交给 Basispoints；图片托管不可用时本地返回中文 400。

## 内嵌图片

Basispoints 只接受公网可拉取的 HTTPS 图片 URL。客户端发来的 base64 图片会落到现有图片存储，并改写成签名链接。需要同时满足：

- `IMAGE_ASSET_PUBLIC_BASE_URL` 是 OpenAI 服务器能够访问的 `https://` 地址
- `IMAGE_ASSET_SIGNING_SECRET` 固定不变
- 图片目录可写，Docker 部署要持久化 `/data`

未配置时，默认回退到原 Codex 通道。单张解码后不超过 10MB，入站图片保留 24 小时。
