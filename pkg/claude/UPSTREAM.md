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

## TypeScript/SDK → Go 职责映射

| 固定源码或 symbol | Go 文件/职责 | 纳入内容 |
| --- | --- | --- |
| `src/acp-agent.ts` `initialize`、`newSession`、`resumeSession`、`loadSession`、`createSession` | `agent.go`、`session.go` | 精确能力声明、每 Session 独立进程、new/load/resume/close 与配置快照 |
| `src/acp-agent.ts` `prompt`、`runConsumer`、`cancel`、`steer` | `session.go`、`steering.go`、`events.go` | 有界 FIFO、user UUID echo、result/idle、cancel drain、priority steering 和迟到消息隔离 |
| `src/acp-agent.ts` `replaySessionHistory` | `history.go` | 本地 transcript 顺序回放、user/assistant/tool 消息与 local-command marker 过滤 |
| `src/acp-agent.ts` `canUseTool`、`src/tools.ts`、`src/elicitation.ts` | `permission.go`、`events.go`、`elicitation.go` | tool call mapper、`session/requestPermission`、AskUserQuestion Form Elicitation、严格响应校验和异常 fail closed |
| `src/acp-agent.ts` `buildConfigOptions`、`applyConfigOptionValue`、`applyFastMode` | `config.go` | model、effort、fast、permission mode 选项及运行时 control 更新 |
| `src/settings.ts` 的 permission mode 解析、`ALLOW_BYPASS` 与 create-session options | `config.go`、`launch.go`、`bypass*.go` | 默认安全模式、root/sandbox 门槛、`--allow-dangerously-skip-permissions` 与 `bypassPermissions` control 更新 |
| Agent SDK `ProcessTransport.initialize` | `launch.go`、`process.go`、`executable.go` | stream-json 输入输出、permission stdio、partial/replay、session/resume、MCP、additional directories 与环境隔离 |
| Agent SDK `SDKControlRequest`、`SDKControlResponse`、`SDKControlCancelRequest` | `protocol/protocol.go`、`transport.go` | control envelope、唯一 request ID、pending 关联、取消和 fatal fan-out |
| Agent SDK `SDKMessage` union | `protocol/protocol.go`、`events.go` | discriminator-first 解码、文本/思考、usage、task、tool 与未知消息兼容 |
| Agent SDK `CanUseTool`、`PermissionResult` | `protocol/protocol.go`、`permission.go` | `can_use_tool` 请求字段、allow/deny 响应与 permission suggestions |
| acp-go-sdk v0.13.5 `Agent`、`AgentLoader`、`ExtensionMethodHandler` | `agent.go`、`../acpserver/server.go` | 外层 ACP connection、load、steering 扩展和统一关闭；不复制 ACP JSON-RPC |

## 冻结 fixture 与测试映射

| 固定证据 | 仓库 fixture/测试 | 锁定行为 |
| --- | --- | --- |
| Agent SDK 0.3.232 `SDKMessage` union 与 stream consumer cases | `protocol/testdata/session-turn.jsonl`、`TestFrozenSessionFixture` | init、user echo、stream delta、assistant、tool progress/result、result、idle 与未知字段兼容 |
| Agent SDK `ProcessTransport.initialize` 参数构造 | `launch_test.go` | 基础 stream-json 参数、permission stdio、partial/replay、session/resume、MCP 与 `--add-dir` |
| create-session、additional-roots 与 client MCP tests | `launch_test.go`、`agent_runtime_test.go` | cwd/目录校验、stdio/HTTP/SSE MCP、进程双通道握手与独立 Session |
| prompt、stream、tool、task、usage 和 permission tests | `agent_runtime_test.go` | assembled/stream 去重、工具终态、基础 plan、usage、权限 allow-once 与 cancel |
| AskUserQuestion elicitation 与 bypass permission tests | `elicitation_test.go`、`config_test.go`、`agent_runtime_test.go` | form schema、单选/多选/Other、answers 回写、能力缺失/取消 fail-closed，以及危险模式声明和运行时切换 |
| session-load local-command marker cases | `history_test.go` | load 回放保留真实文本并删除内部命令标签 |
| Agent SDK control request/response/cancel 声明 | `protocol/protocol_test.go`、`transport_test.go` | wire 形状、pending 配对、反向 control、坏帧继续和超限帧 fatal |

fixture 只保留无凭据、无真实用户路径、无真实 transcript 的最小消息。`session-turn.jsonl` 按固定公开声明裁剪，不提交 npm 包或上游测试目录。

## Go 等价改写与已知边界

- TypeScript 通过 Agent SDK `query()` 持有子进程；Go 直接持有 `exec.Cmd`、stdin/stdout/stderr 和 control transport，但仍保持一个 ACP Session 对应一个持续 CLI 进程。
- TypeScript 的 async iterable 和单事件循环由单 stdout reader、单写锁、有界 prompt channel、活动 turn 身份和 cancel epoch 表达。前台 cancel 可立即返回；下一 turn 必须等待旧 turn 的 result/idle，缺少尾帧会关闭 Session。
- TypeScript `getSessionMessages` 读取 CLI transcript；Go 从 `CLAUDE_CONFIG_DIR` 或 `~/.claude/projects/*/<session-id>.jsonl` 读取，限制单行 8 MiB、单文件 64 MiB，并只回放 user/assistant/tool V1 内容。
- 初始化同时要求 control `initialize` 成功和 `system/init` Session ID 一致；任何一侧失败都不安装半初始化 Session。
- 进程输出单帧、stderr 尾部、pending control、并发反向 control 和 prompt FIFO 都有硬上限；日志不记录 prompt、MCP header/env 或原始协议帧。
- fast 使用 ACP select `on/off`；`bypassPermissions` 只在非 root 或 `IS_SANDBOX` 非空时提供，并通过 CLI allow flag 加 Session control mode 生效。进程安全门槛不允许时，CLI 报告的危险初始模式会收紧为 `default`。
- AskUserQuestion 只在客户端声明 Form Elicitation 时转换为结构化表单；单选、多选、每题 Other 与 decline 会写回工具 `answers`，cancel、未知响应、迟到 Session/Turn 和请求失败均拒绝工具调用。
- Image 使用 base64 或 URI source；ResourceLink 转为带来源属性的文本上下文，embedded text/blob 转为 document source；Audio 明确返回错误。

## 明确跳过

- session list/fork/delete、providers、goal、结构化 session failure、文件变更审计、富 subagent transcript、slash command 列表、ACP terminal、MCP/URL elicitation、认证/登出；
- post-v0.70 permission extension、clear-context planning 及不在固定 control 声明中的新 subtype；
- SDK 内嵌 MCP server、hooks、dialog、provider routing、custom agent、插件和完整设置透传；
- 真实账号、网络或付费调用作为默认测试条件。

## 增量同步步骤

1. 在独立变更中更新 `.upstream/claude-agent-acp` tag/commit，并固定该版本使用的 ACP TS SDK 与 Agent SDK 解析版本。
2. 对比 `src/acp-agent.ts`、`src/tools.ts`、`src/settings.ts`、相关 tests，以及 Agent SDK `sdk.d.ts` 的消息、control 和启动选项声明。
3. 先更新最小冻结 fixture 和本文映射，运行协议与 fake CLI 测试，确认差异是新增字段、行为变化还是破坏性变更。
4. 更新 `protocol` 窄类型，再修改 transport、Session 状态机、mapper 与能力声明；未知字段继续通过 `json.RawMessage` 保留。
5. 运行中文注释审计、`gofmt`、`go test ./...`、`go test -race ./...`、`go vet ./...` 和目标平台构建；真实 CLI smoke 只在显式开关与已有认证环境中执行并单独记录。

## 同步记录

- 2026-08-24：固定 claude-agent-acp v0.70.0/`d0aafb1`、ACP TS SDK 1.3.0 与 Agent SDK 0.3.232；建立原生 Go stream-json/control 窄协议、per-session CLI、fake CLI/冻结 fixture、Session/prompt/cancel/steering、事件/tool/permission/config/MCP/additional directories 和 load 历史回放追踪。
- 2026-08-24：按固定 upstream 补齐 root/sandbox gated `bypassPermissions`、`AskUserQuestion` Form Elicitation、发布版本注入和稳定 Session `ResourceNotFound`；MCP/URL Elicitation 仍保持未实现。
