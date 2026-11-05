# Claude 上游基线与同步说明

本文档是 Claude Adapter 唯一的上游追溯入口。协议声明、运行时行为和冻结 fixture 必须先回到这里列出的固定源码核对，不能以 README 或二手摘要替代源码。Codex 的独立基线见 [`../codex/UPSTREAM.md`](../codex/UPSTREAM.md)。

## 固定版本

| 上游 | 固定版本 | 固定提交或解析版本 | 用途 |
| --- | --- | --- | --- |
| `github.com/coder/acp-go-sdk` | v0.13.5 | `0845a3bb9eddda5bfc22a94dd3598c90cb842451` | ACP 连接、分发、取消和扩展基础；本项目不复制其内部协议实现 |
| `github.com/agentclientprotocol/claude-agent-acp` | v0.70.0 | `d0aafb1ca26427285ffaeac8d8a4452fff28e9c3` | Session、prompt、事件、权限和配置行为基线 |
| `@agentclientprotocol/sdk` | 1.3.0 | claude-agent-acp v0.70.0 `package-lock.json` 解析值 | TypeScript ACP surface 参考 |
| `@anthropic-ai/claude-agent-sdk` | 0.3.232 | claude-agent-acp v0.70.0 `package-lock.json` 解析值 | Claude CLI stream-json/control 公开声明、启动参数与消息类型来源 |

本地参考仓库位于项目根目录相对路径 `.upstream/claude-agent-acp`。同步前必须执行：

```sh
git -C .upstream/claude-agent-acp describe --tags --exact-match
git -C .upstream/claude-agent-acp rev-parse HEAD
```

结果必须分别为 `v0.70.0` 与 `d0aafb1ca26427285ffaeac8d8a4452fff28e9c3`。`.upstream/` 由仓库忽略，本地审计使用的 Agent SDK 包、npm tarball、minified bundle 与上游 clone 均不提交。

## 原生客户端

`protocol/protocol.go` 识别 SDK stream-json 与 control 信封，`transport.go` 管理请求关联，`launch.go` 管理每会话启动约束。`session.go`、`turn.go` 和 `native_execution.go` 按确切原生会话和本次提交处理接纳、控制与结算。文件和测试入口见 [README.md](README.md)。

## 历史同步记录

- 2026-08-24：固定 claude-agent-acp v0.70.0/`d0aafb1`、ACP TS SDK 1.3.0 与 Agent SDK 0.3.232；建立原生 Go stream-json/control 窄协议、per-session CLI、fake CLI/冻结 fixture、Session/prompt/cancel/steering、事件/tool/permission/config/MCP/additional directories 和 load 历史回放追踪。
- 2026-08-24：按固定 upstream 补齐 root/sandbox gated `bypassPermissions`、`AskUserQuestion` Form Elicitation、发布版本注入和稳定 Session `ResourceNotFound`；MCP/URL Elicitation 仍保持未实现。
- 2026-08-24：补齐调用方 `PrefixArgs`、完整 `Environment` 和 `CLAUDE_CONFIG_DIR` 启动配置；版本探测、Adapter 判断与每个 Session 进程使用同一配置快照。
- 2026-08-25：参考 upstream `toolUpdateFromDiffToolResponse` 补齐 Edit/Write 标准 ACP diff 的多 hunk 完成态修正，并保持 message-level 结果只能归属唯一 tool result 的约束。
- 2026-08-25：参考 upstream `runConsumer`、`snapshotFromUsage`、`totalTokens`、`inferContextWindowFromModel` 与 `getMatchingModelUsage`，补齐顶层 assistant Context Usage、PromptResponse Turn Usage、模型窗口推断和权威缓存。
- 2026-08-25：直接复用 ACP SDK Available Command 类型，并参考 upstream `sendAvailableCommandsUpdate`、`getAvailableSlashCommands`、`commands_changed`、`claudeCodeMetaFromToolUse` 与 `resolveSkillPath`，补齐命令同步和 Skill 工具元数据。
- 2026-08-25：按 upstream `owedTrailingIdles` 补齐 result 后无身份 idle 的跨轮隔离，避免高并发或快速连续 Prompt 时旧 idle 提前结算新 Turn。
- 2026-08-25：按 upstream 两阶段启动行为让 `session/new` 只等待 control initialize，并在首个 Prompt 后消费 `system/init`；按内容块顺序核对流式与聚合 assistant 帧，兼容聚合帧 content index 重排。
- 2026-08-25：参考 upstream `assistant.error`、`result.is_error` 与 `errorKindData` fallback，补齐 Provider 错误、login 和 stop reason 优先级；不引入客户端专属 Session Failure 产品扩展。
- 2026-08-26：按固定 upstream `cancel` 在 query 已关闭时直接成功的语义，补齐 cancel 与并发 CloseSession 的幂等处理和确定性竞态回归；仍保留 interrupt 无响应的有界失败。
- 2026-08-26：核对固定 upstream 的 `agentInfo.version = packageJson.version` 后保持 Adapter 实现版本语义，并通过通用 `agentInfo._meta.runtime.version` 发布构造阶段已探测的 Claude CLI 真实版本。
