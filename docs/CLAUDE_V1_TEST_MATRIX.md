# Claude V1 测试矩阵

Claude 默认测试只使用仓库内 fake CLI 与冻结 JSONL fixture，不读取真实账号、不访问网络，也不会产生模型费用。

| 范围 | 默认证据 | 说明 |
| --- | --- | --- |
| Adapter 选择 | `cmd/acp-agent/main_test.go` | Codex 默认值不变；未选择 Claude 时不探测 Claude；显式坏路径在 ACP stdout 写入前失败 |
| 协议判别与 control | `pkg/claude/protocol/protocol_test.go`、`protocol/testdata/session-turn.jsonl` | 已知/未知消息、字段兼容、request/response/cancel 和权限结果 |
| CLI 发现、启动参数、目录、MCP | `pkg/claude/launch_test.go`、`agent_runtime_test.go` | 显式路径、session/resume、stream-json、additional directories、stdio/HTTP/SSE MCP 与危险权限启动门槛 |
| transport 与进程 | `transport_test.go`、`agent_runtime_test.go` | JSONL、pending 关联、反向 control、坏帧、超限帧、真实 helper-process 生命周期 |
| Session 与 Prompt | `agent_runtime_test.go` | initialize、new、FIFO、文本去重、result/idle、cancel、close |
| 事件与工具 | `agent_runtime_test.go`、`tool_mapper_test.go` | assistant、tool start/progress/result、task plan、usage；Task、文件、搜索、Web、Skill、AskUserQuestion 与 MCP 使用结构化 ACP kind/title/content/rawInput |
| 权限与配置 | `agent_runtime_test.go`、`config_test.go` | `can_use_tool`、allow-once、model、mode、`bypassPermissions` 门槛/收紧与 control response |
| AskUserQuestion Elicitation | `elicitation_test.go`、`TestClaudeAgentSessionPromptPermissionConfigAndCancel` | form 能力协商、单选/多选/Other、answers 回写、取消与未知响应 fail-closed |
| Session 缺失错误 | `TestClaudeAgentReturnsResourceNotFoundForMissingSession` | Prompt/config/mode/steering 与已知恢复缺失统一为 ACP `ResourceNotFound` |
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
