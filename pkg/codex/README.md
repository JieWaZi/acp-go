# Codex

`codex.NewClient` 冻结一个 Codex App Server 启动计划。 `Client` 管理进程、原生 thread、turn、控制请求及原生结果收据。直接 Go 调用与 `acp-agent --adapter codex` 共用该客户端。

- `client.go` 管理传输、握手、请求关联和连接生命周期。
- `session.go` 与 `turn.go` 管理原生会话、执行接纳、取消和 steering。
- `events.go`、`outcomes.go`、`history.go` 转换原生事件、终态和历史。
- `management.go` 处理模型、设置、账号、配额和原生管理操作。
- [protocol](protocol) 保存协议类型与生成定义。生成方式和来源见 [UPSTREAM.md](UPSTREAM.md)。

`Options.Executable` 为空时查找 `codex`。宿主可以用 `CODEX_HOME` 隔离原生配置，使用 `Options.StartupConfig` 设置启动配置。SDK 会话配置和用量以原生回执为准。保留原生结果与回调交付错误的区别，支持能力协商后的终态对账。

定向验证使用 `go test ./pkg/codex ./pkg/codex/protocol ./cmd/acp-agent`。
