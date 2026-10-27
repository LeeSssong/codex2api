# 凭证运营生产发布

2026-10-18 23:08（Asia/Shanghai）已在现有 Codex2API 源站原地更新。

- 入口：[凭证运营](https://codex.xingqiaolab.top/admin/credential-ops)。普通账号添加也包含「2FA 登录导入」。
- 应用版本：`release-20261018-credential-ops-f7bdc26`，Linux AMD64；运行包打包修正：`f3fbe0f`。
- 公网健康和管理台 200；凭证运营、加密、Mihomo 状态接口 200。托管 Worker 在线，密码/2FA、邮箱 OTP、Excel 与 2FA 更换能力齐备。独立凭证加密已启用。
- 真实生产页面验证了运营中心及首次导入密码/TOTP 输入框，没有提交真实上游登录或更换 2FA。
- 75 条账号、1 条 API Key、6156 条用量记录数量保持不变。可用账号为 0，原有软删除账号没有恢复。
- 备份：`/opt/codex2api/backups/20261018-230823-credential-operations-f7bdc26`。包含停写数据库备份、原配置、数据/日志/密钥与旧容器信息；旧镜像保留。
- 其它容器 ID 与启动时间不变。AWS、vm2api、Sub2API、DNS 与反向代理配置未修改。

AMD64 Worker 构建内 31 项 Python 和 9 项 Node 检查通过。目标主机原生预检发现 `libdl.so.2` 缺失后阻止替换，补齐后重新验证 Worker、Node/Python 与 chrome146 均通过，再执行部署。运行包 SHA256 清单：`f8ad00402258db08cfafd014f4bb222337aca072b9cfe45699fc9c49f7949367`。

源码恢复情况和真实上游验收边界仍参见[迁移验收记录](../credential-operations-validation.md)。完整发布证据在本机 `/Users/gongtengxinwen/.codex/release-evidence/codex2api/20261018-credential-operations/`，不包含打印的管理密钥或账号凭据。
