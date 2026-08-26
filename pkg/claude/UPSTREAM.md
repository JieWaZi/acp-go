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
| `src/acp-agent.ts` `createSession` 的 `options.env`、`pathToClaudeCodeExecutable` 与 `extraArgs` | `agent.go`、`executable.go`、`process.go`、`session.go` | 调用方前置参数、完整环境合并、版本探测、Adapter 配置读取与每 Session 进程启动 |
| `src/acp-agent.ts` `prompt`、`runConsumer`、`cancel`、`steer` | `session.go`、`steering.go`、`events.go`、`usage.go` | 有界 FIFO、user UUID echo、result/idle、cancel drain、priority steering、迟到消息隔离、顶层 assistant Usage 快照、权威模型窗口，以及 `assistant.error`/`result.is_error` Provider 失败 |
| `src/acp-agent.ts` `sendAvailableCommandsUpdate`、`getAvailableSlashCommands`、`commands_changed` 分支 | `commands.go`、`events.go`、`protocol/protocol.go` | 初始化与动态 Slash Command 全量替换、终端/静态过滤、MCP 重命名和参数提示 |
| `src/acp-agent.ts` `claudeCodeMetaFromToolUse`、`resolveSkillPath` | `tool_mapper.go`、`events.go`、`permission.go` | 全部工具的 `claudeCode.toolName`，以及 Skill 名称和项目、目录作用域、插件、用户目录路径 |
| `src/acp-agent.ts` `replaySessionHistory` | `history.go` | 本地 transcript 顺序回放、user/assistant/tool 消息与 local-command marker 过滤 |
| `src/acp-agent.ts` `canUseTool`、`src/tools.ts`、`src/elicitation.ts` | `permission.go`、`events.go`、`tool_mapper.go`、`elicitation.go` | tool call mapper、Edit/Write 乐观 diff、`structuredPatch` 完成态修正、`session/requestPermission`、AskUserQuestion Form Elicitation、严格响应校验和异常 fail closed |
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
| `cancel` 的 query-closed no-op 与 interrupt 等待 cases | `TestClaudeAgentCancelConcurrentCloseIsIdempotent`、`TestClaudeAgentCancelUnresponsiveInterrupt`、`agent_runtime_test.go` | cancel 与并发 CloseSession 幂等；仍存活 Session 的 interrupt 无响应必须有界失败 |
| `runConsumer` 的 `message_start`、`message_delta` 与 `result.modelUsage` cases | `TestClaudeAgentUsageSeparatesContextSnapshotFromTurnTotals`、`usage_test.go` | 可空累计字段、四类 token 的 Context Usage、PromptResponse Turn Usage、1M 推断、权威窗口与跨 Session 缓存 |
| `runConsumer` 的 `assistant.error`、`result.is_error`、login 与 stop reason cases | `TestPromptResponseFromResultMatchesUpstreamErrorSemantics`、`agent_runtime_test.go` | Provider `errorKind`、ACP InternalError/AuthRequired、max_tokens/refusal 优先和非错误 subtype stop reason |
| `sendAvailableCommandsUpdate`、`commands_changed`、`getAvailableSlashCommands` cases | `TestClaudeAgentSessionPromptPermissionConfigAndCancel`、`TestClaudeAgentPublishesCommandsAfterResumeAndLoad`、`commands_test.go` | New/Load/Resume 后初始命令、动态完整替换、terminal/unsupported 过滤、MCP 名称和 input hint |
| Skill tool metadata/path cases | `TestSkillToolCallIncludesClaudeMetadata`、`TestResolveClaudeSkillPathMatchesUpstreamLayouts` | 标准 ACP ToolCall 的 `claudeCode` 元数据及四类固定 upstream 路径布局 |
| AskUserQuestion elicitation 与 bypass permission tests | `elicitation_test.go`、`config_test.go`、`agent_runtime_test.go` | form schema、单选/多选/Other、answers 回写、能力缺失/取消 fail-closed，以及危险模式声明和运行时切换 |
| session-load local-command marker cases | `history_test.go` | load 回放保留真实文本并删除内部命令标签 |
| `src/tools.ts` 的 `toolUpdateFromDiffToolResponse` 与多 hunk tests | `TestToolDiffUpdateFromResultMatchesClaudeUpstream`、`TestCompleteDiffToolUsesStructuredPatch`、`TestCompleteDiffToolWithoutStructuredPatchKeepsOptimisticDiff` | 唯一 tool result 携带的 `filePath/structuredPatch` 转换为标准 ACP diff 和 locations；缺失结构化结果时不覆盖 Edit/Write 开始态 diff |
| Agent SDK control request/response/cancel 声明 | `protocol/protocol_test.go`、`transport_test.go` | wire 形状、pending 配对、反向 control、坏帧继续和超限帧 fatal |

fixture 只保留无凭据、无真实用户路径、无真实 transcript 的最小消息。`session-turn.jsonl` 按固定公开声明裁剪，不提交 npm 包或上游测试目录。

## Go 等价改写与已知边界

- TypeScript 通过 Agent SDK `query()` 持有子进程；Go 直接持有 `exec.Cmd`、stdin/stdout/stderr 和 control transport，但仍保持一个 ACP Session 对应一个持续 CLI 进程。
- 固定 upstream 的 `initialize` 把 `agentInfo.version` 设为 `packageJson.version`，即 Adapter 实现版本。Go Adapter 保持该标准字段语义，并把启动时通过 `claude --version` 已探测到的被包装 CLI 版本放入通用 `agentInfo._meta.runtime.version`；客户端不得用 Adapter 的本地 `development` 构建标识代替 CLI 版本。
- TypeScript 把调用方 `env` 合并到进程环境并通过 `extraArgs` 扩展 CLI 参数；Go `Config` 接收调用方已合并的完整 `Environment` 与有序 `PrefixArgs`，并同时用于版本探测、Adapter 配置读取和每个 Session 进程。stream-json、权限、MCP 等协议参数仍由 Adapter 追加。
- TypeScript 的 async iterable 和单事件循环由单 stdout reader、单写锁、有界 prompt channel、活动 turn 身份和 cancel epoch 表达。前台 cancel 可立即返回；下一 turn 必须等待旧 turn 的 result/idle，缺少尾帧会关闭 Session。若 CloseSession 在 interrupt 等待期间关闭 transport，则按 upstream 的 query-closed no-op 语义将 cancel 视为幂等成功；仍存活 Session 的 interrupt 超时或 CLI 错误不得吞掉。
- 非 steering `result` 会按 upstream `owedTrailingIdles` 记录无 Turn ID 的尾随 idle；旧 idle 即使晚于下一轮激活，也只偿还旧轮次债务，不会误判新轮次缺少 result。
- TypeScript `getSessionMessages` 读取 CLI transcript；Go 从 `CLAUDE_CONFIG_DIR` 或 `~/.claude/projects/*/<session-id>.jsonl` 读取，限制单行 8 MiB、单文件 64 MiB，并只回放 user/assistant/tool V1 内容。
- Session 创建与固定 upstream 一致，只等待 control `initialize` 成功；Claude CLI 在首个 Prompt 后发送的 `system/init` 用于异步校准 Session ID、模型、权限模式与 Slash Command，不阻塞 `session/new`。
- 进程输出单帧、stderr 尾部、pending control、并发反向 control 和 prompt FIFO 都有硬上限；日志不记录 prompt、MCP header/env 或原始协议帧。
- fast 使用 ACP select `on/off`；`bypassPermissions` 只在非 root 或 `IS_SANDBOX` 非空时提供，并通过 CLI allow flag 加 Session control mode 生效。进程安全门槛不允许时，CLI 报告的危险初始模式会收紧为 `default`。
- AskUserQuestion 只在客户端声明 Form Elicitation 时转换为结构化表单；单选、多选、每题 Other 与 decline 会写回工具 `answers`，cancel、未知响应、迟到 Session/Turn 和请求失败均拒绝工具调用。
- Edit/Write 在 tool use 阶段先发送标准 ACP diff；当唯一 tool result 的 message-level `tool_use_result` 携带 upstream PostToolUse 同形的 `filePath/structuredPatch` 时，用多 hunk diff 和 locations 替换乐观内容。当前 CLI 边界不注入通用 hooks；没有结构化结果时保持开始态内容，不用结果文本覆盖 diff。
- Image 使用 base64 或 URI source；ResourceLink 转为带来源属性的文本上下文，embedded text/blob 转为 document source；Audio 明确返回错误。
- `usage_update` 只由顶层 assistant 的累计消息用量驱动，四类 token 共同表示当前上下文占用；`result.usage` 只生成 PromptResponse 的本轮累计量，`result.modelUsage` 负责确认并缓存当前模型窗口，不用 Turn 累计量覆盖 Context 快照。
- Slash Command 直接使用 control initialize 与 `commands_changed` 的 Claude SDK 权威列表，通过 ACP SDK 的 `AvailableCommand`/`available_commands_update` 发布；不维护 Adapter 私有命令表。Skill 路径探测逐项对齐固定 upstream，路径不存在时只省略链接元数据。
- assistant 聚合帧按 upstream 的内容块文档顺序核对已发送 stream delta，不依赖聚合帧重新编号后的 content index；顶层与不同 subagent 的去重状态按父 ToolCall 隔离。
- 顶层 assistant 的 Agent SDK `error` 类别通过 ACP InternalError `data.errorKind` 原样发布；`result.is_error` 不会伪装成成功 PromptResponse，login 结果使用标准 AuthRequired。未协商 upstream 私有 Session Failure 扩展时保持其 fallback 错误语义。

## 明确跳过

- session list/fork/delete、providers、goal、结构化 session failure、文件变更审计、富 subagent transcript、ACP terminal、MCP/URL elicitation、认证/登出；
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
- 2026-08-24：补齐调用方 `PrefixArgs`、完整 `Environment` 和 `CLAUDE_CONFIG_DIR` 启动配置；版本探测、Adapter 判断与每个 Session 进程使用同一配置快照。
- 2026-08-25：参考 upstream `toolUpdateFromDiffToolResponse` 补齐 Edit/Write 标准 ACP diff 的多 hunk 完成态修正，并保持 message-level 结果只能归属唯一 tool result 的约束。
- 2026-08-25：参考 upstream `runConsumer`、`snapshotFromUsage`、`totalTokens`、`inferContextWindowFromModel` 与 `getMatchingModelUsage`，补齐顶层 assistant Context Usage、PromptResponse Turn Usage、模型窗口推断和权威缓存。
- 2026-08-25：直接复用 ACP SDK Available Command 类型，并参考 upstream `sendAvailableCommandsUpdate`、`getAvailableSlashCommands`、`commands_changed`、`claudeCodeMetaFromToolUse` 与 `resolveSkillPath`，补齐命令同步和 Skill 工具元数据。
- 2026-08-25：按 upstream `owedTrailingIdles` 补齐 result 后无身份 idle 的跨轮隔离，避免高并发或快速连续 Prompt 时旧 idle 提前结算新 Turn。
- 2026-08-25：按 upstream 两阶段启动行为让 `session/new` 只等待 control initialize，并在首个 Prompt 后消费 `system/init`；按内容块顺序核对流式与聚合 assistant 帧，兼容聚合帧 content index 重排。
- 2026-08-25：参考 upstream `assistant.error`、`result.is_error` 与 `errorKindData` fallback，补齐 Provider 错误、login 和 stop reason 优先级；不引入客户端专属 Session Failure 产品扩展。
- 2026-08-26：按固定 upstream `cancel` 在 query 已关闭时直接成功的语义，补齐 cancel 与并发 CloseSession 的幂等处理和确定性竞态回归；仍保留 interrupt 无响应的有界失败。
- 2026-08-26：核对固定 upstream 的 `agentInfo.version = packageJson.version` 后保持 Adapter 实现版本语义，并通过通用 `agentInfo._meta.runtime.version` 发布构造阶段已探测的 Claude CLI 真实版本。
