# OpenClaw 协议来源与兼容基线

核对基线为 npm `openclaw` 2026.11.7（MIT）及 Node.js 26.1.0 的 macOS arm64 安装包。适配器启动用户安装的 OpenClaw，复用原生 `openclaw acp --no-prefix-cwd` 和 `openclaw gateway call --expect-url … --json`。ACP 使用本仓库的共享客户端和 `JieWaZi/acp-go-sdk`。 [上游仓库](https://github.com/openclaw/openclaw) · [npm 发布包](https://www.npmjs.com/package/openclaw/v/2026.11.7) · [ACP CLI](https://docs.openclaw.ai/cli/acp) · [Gateway CLI](https://docs.openclaw.ai/cli/gateway)

## 连接与会话

- 原生配置读取确认本机端口和 TLS。适配器将 ACP 与 Gateway CLI 绑定到同一字面回环地址。普通本机配置保留原生令牌、密码和 TLS 证书校验。私有配置保留直接 JSON 中的非路由变量。若 JSON5 或间接引入中的其他 `env.vars` 值只能从脱敏输出取得，启动会报错，可改用所选配置目录的 `.env` 或已导出的环境变量。
- `sessions.create`、`sessions.describe` 与 ACP resume 用于创建和绑定会话。续聊校验原生会话键、物理 ID、工作目录和本机放置。打开失败时，仅用精确的键和 `expectedSessionId` 补偿删除已确认新建的行。模型用 `sessions.patch` 和回执确认。思考档位来自 `models.list` 的原生元数据和 ACP `thought_level`。审批由 Gateway 的权限策略负责。
- 发布包 `src/cli/gateway-cli/gateway-action.ts`、`src/gateway/call.ts` 与 `src/gateway/server/ws-connection/auth-messages.ts` 对应的已发布实现定义了 `gateway call --json` 的失败信封。适配器使用固定凭据类型、明确的 RPC 错误标识和经过核对的本地 WebSocket 关闭原因归类。只有确定缺失的会话身份使用会话缺失分类。厂商诊断正文、stderr 和回执附加数据保持私有。
- `sessions.create` 的分叉参数 `forkFrom` 只接受 `last-completed`。适配器关闭新建分叉，旧回执可继续核对。每会话 MCP、额外工作目录和权限覆盖不在已验证能力内。

## 用量与验证

2026.11.7 的状态库和 Agent 库分别使用 schema 36、38。适配器对照 ACP replay 与 transcript 的同一原生运行，仅累计已识别响应的输出 token。缺失或证据不匹配时保留未知。聚合结果不代表全部调用或费用覆盖。

在隔离配置与仅回环网络中，通过本地合成模型验证了配置令牌、TLS 密码、模型切换、原生思考档位、连续发送、取消、冷恢复和用量。另用受控 ACP 错误触发打开失败，真实 Gateway 中无遗留的会话行。发布包错误分类由源码与定向测试核对。真实账号、外部模型、Windows、Linux 和其他 OpenClaw 版本仍需单独验收。
