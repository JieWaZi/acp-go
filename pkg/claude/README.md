# Claude Code

`claude.NewClient` 冻结 Claude Code 的可执行文件、完整环境和会话启动规则。`Client` 为每个打开的原生会话管理一个 stream-json 进程。直接 Go 调用与 `acp-agent --adapter claude` 共用该客户端。

- `client.go` 和 `session.go` 管理初始化、会话和关闭。
- `launch.go`、`transport.go` 管理原生启动参数及 stream-json/control 通道。
- `turn.go`、`native_execution.go`、`outcomes.go` 处理执行接纳、取消和结果对账。
- `interaction.go` 管理工具权限与原生问答，`settings.go` 管理会话有效设置。
- `events.go`、`assistant_output.go`、`usage.go`、`history.go` 转换原生消息和计量。

`Options.Executable` 默认为 `claude`。配置与历史根使用原生 `CLAUDE_CONFIG_DIR`。项目目录、MCP、权限与可用工具以每个会话的启动约束和原生回执为准。

定向验证使用 `go test ./pkg/claude ./pkg/claude/protocol`。协议来源见 [UPSTREAM.md](UPSTREAM.md)。
