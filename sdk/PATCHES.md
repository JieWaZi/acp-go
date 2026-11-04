# SDK 维护说明

本子模块基于 coder/acp-go-sdk v0.13.5，保留上游协议类型，由 acp-go 仓库以独立模块路径 `github.com/JieWaZi/acp-go/sdk` 发布；Adapter 与宿主直接引用同一固定版本，不再需要 Go replace。Apache-2.0 许可与版权见 [LICENSE](LICENSE)。生成协议文件保持原样。

`connection.go` 与 `client.go` 提供显式启用的通知背压：条数与原始帧字节共同限制队列，响应屏障和通知顺序保持一致；传输由独立所有者关闭以解除阻塞。`connection_backpressure_test.go` 覆盖突发、双向请求、响应屏障、预算与取消。

`tool_call_update.go` 保留非 nil 空 Content/Locations 的 `[]` 编码语义，nil 时省略，允许工具更新明确清空集合。

`errors.go` 在上游 JSON-RPC 错误出口增加 `errors.As`，保留 Adapter 包装后的 `RequestError` code、message 和 data。普通错误与取消保留原映射，回归见 `errors_test.go`。

`agent.go` 与手写 `extensions.go` 增加 `SessionLifecycleOwner` opt-in。接管者直接收到同样校验的 `session/prompt` 和 `session/cancel`。SDK 保留请求和连接取消，session 级排他与原生中断由 Adapter 决定。默认继续走上游生成 dispatch。验证见 `session_lifecycle_test.go`。

本地验证在此目录运行 `GOWORK=off go test -race .`；上层 Adapter 与使用方须引用同一 SDK 版本。
