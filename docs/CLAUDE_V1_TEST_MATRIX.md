# Claude V1 测试矩阵

Claude 默认测试只使用仓库内 fake CLI 与冻结 JSONL fixture，不读取真实账号、不访问网络，也不会产生模型费用。

| 范围 | 默认证据 | 说明 |
| --- | --- | --- |
| Adapter 选择 | `cmd/acp-agent/main_test.go` | Codex 默认值不变；未选择 Claude 时不探测 Claude；显式坏路径在 ACP stdout 写入前失败 |
| 协议判别与 control | `pkg/claude/protocol/protocol_test.go`、`protocol/testdata/session-turn.jsonl` | 已知/未知消息、字段兼容、request/response/cancel 和权限结果 |
| CLI 发现、启动参数、目录、MCP | `pkg/claude/launch_test.go`、`agent_runtime_test.go`、`process_test.go` | 显式路径、调用方前置参数、完整环境、`CLAUDE_CONFIG_DIR`、session/resume、stream-json、additional directories、stdio/HTTP/SSE MCP 与危险权限启动门槛 |
| transport 与进程 | `transport_test.go`、`agent_runtime_test.go` | JSONL、pending 关联、反向 control、坏帧、超限帧、真实 helper-process 生命周期 |
| Session 与 Prompt | `agent_runtime_test.go`、`events_test.go` | control initialize 后立即完成 new、首个 Prompt 后消费 system/init、FIFO、按内容块顺序文本去重、聚合 index 重排、result/idle、尾随 idle 债务隔离、cancel 与并发 close 幂等、无响应 interrupt 有界失败 |
| Slash Command | `agent_runtime_test.go`、`commands_test.go`、`protocol_test.go` | New/Load/Resume 初始列表、`commands_changed` 动态完整替换、terminal/unsupported 过滤、MCP 重命名与参数提示；对外直接使用 ACP SDK 类型 |
| 事件与工具 | `agent_runtime_test.go`、`tool_mapper_test.go`、`history_test.go` | assistant、tool start/progress/result、task plan、usage；Task、文件、搜索、Web、Skill、AskUserQuestion 与 MCP 使用结构化 ACP kind/title/content/rawInput；Edit/Write 覆盖开始态 diff、`structuredPatch` 多 hunk 修正和缺失结构化结果时不覆盖 |
| Skill 元数据 | `tool_mapper_test.go` | ToolCall `claudeCode.toolName/skill/skillPath`，以及项目、目录作用域、插件和用户级 `SKILL.md` 布局 |
| 权限与配置 | `agent_runtime_test.go`、`config_test.go` | `can_use_tool`、allow-once、model、mode、`bypassPermissions` 门槛/收紧与 control response |
| AskUserQuestion Elicitation | `elicitation_test.go`、`TestClaudeAgentSessionPromptPermissionConfigAndCancel` | form 能力协商、单选/多选/Other、answers 回写、取消与未知响应 fail-closed |
| Session 缺失错误 | `TestClaudeAgentReturnsResourceNotFoundForMissingSession` | Prompt/config/mode/steering 与已知恢复缺失统一为 ACP `ResourceNotFound` |
| Provider 失败 | `events_test.go`、`agent_runtime_test.go` | `assistant.error` + `result.is_error` 转 ACP InternalError `data.errorKind`，login 转 AuthRequired，max_tokens/refusal 保持标准 stop reason |
| 输入与历史 | `content_test.go`、`history_test.go` | Text/Image/Resource、Audio 拒绝、load 回放与本地命令标记过滤 |
| 中文注释 | `pkg/codex/comment_audit_test.go` | 全仓手写类型、字段和函数的中文注释审计 |

发布前运行：

```sh
gofmt -w ./pkg/claude ./cmd/acp-agent
go test ./... -count=1 -timeout=300s
go test -race -p=1 ./... -count=1 -timeout=600s
go vet ./...
go run ./tools/protocolgen --check
```

真实 Claude CLI smoke 只用于人工/显式环境验证：调用者必须已经安装并登录 CLI，并自行确认请求可能联网和计费。默认 CI 不应设置真实 `CLAUDE_CODE_EXECUTABLE` 或发送真实 Prompt；未执行真实 smoke 时，报告必须明确写为“fake CLI/构建已验证，真实 CLI 未验证”。
