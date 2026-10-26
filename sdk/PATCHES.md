# SDK 维护说明

本子模块基于 coder/acp-go-sdk v0.13.5，保留 `github.com/coder/acp-go-sdk` 的模块路径与协议类型，由 acp-go 仓库维护并以 `github.com/JieWaZi/acp-go/sdk` 版本供 Go replace 引用。Apache-2.0 许可与版权见 [LICENSE](LICENSE)。生成协议文件保持原样。

`connection.go` 与 `client.go` 提供显式启用的通知背压：条数与原始帧字节共同限制队列，响应屏障和通知顺序保持一致；传输由独立所有者关闭以解除阻塞。`connection_backpressure_test.go` 覆盖突发、双向请求、响应屏障、预算与取消。

`tool_call_update.go` 保留非 nil 空 Content/Locations 的 `[]` 编码语义，nil 时省略，允许工具更新明确清空集合。

本地验证在此目录运行 `GOWORK=off go test -race .`；上层 Adapter 与使用方须引用同一 SDK 版本。
