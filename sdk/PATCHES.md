# SDK 维护说明

本子模块基于 coder/acp-go-sdk v0.13.5，保留上游协议类型，由 acp-go 仓库以独立模块路径 `github.com/JieWaZi/acp-go/sdk` 发布；Adapter 与宿主直接引用同一固定版本，不再需要 Go replace。Apache-2.0 许可与版权见 [LICENSE](LICENSE)。生成协议文件保持原样。

`connection.go` 与 `client.go` 提供显式启用的通知背压：条数与原始帧字节共同限制队列，响应屏障和通知顺序保持一致；传输由独立所有者关闭以解除阻塞。`connection_backpressure_test.go` 覆盖突发、双向请求、响应屏障、预算与取消。

`tool_call_update.go` 保留非 nil 空 Content/Locations 的 `[]` 编码语义，nil 时省略，允许工具更新明确清空集合。

`errors.go` 在上游 JSON-RPC 错误出口增加 `errors.As`，保留 Adapter 包装后的 `RequestError` code、message 和 data。普通错误与取消保留原映射，回归见 `errors_test.go`。

`agent.go` 与手写 `extensions.go` 增加 `SessionLifecycleOwner` opt-in。接管者直接收到同样校验的 `session/prompt` 和 `session/cancel`。SDK 保留请求和连接取消，session 级排他与原生中断由 Adapter 决定。默认继续走上游生成 dispatch。验证见 `session_lifecycle_test.go`。

本地验证在此目录运行 `GOWORK=off go test -race .`；上层 Adapter 与使用方须引用同一 SDK 版本。

`connection.go` 的 `SendRequestWithWriteHooks` 使用既有 pending 请求所有者，在同一连接写入门内执行同步准入及真实字节回执。写入等待 context 可与原生响应等待分开；取消发生在编码或写入准入前时不提交请求。部分写入或写入错误使连接失效，避免后续帧复用损坏的 JSONL 流。所有请求、通知、响应共用该写入门，既有通知水位屏障保留。验证见 `connection_write_test.go`。

`pkg/nativeacp` 消费这些钩子，以同一 Prompt 所有者处理准备、FIFO／拒绝并发、交互取消、原生终态和有序取消写入。未提交的 Prompt 不发送原生 cancel；已提交后等待真实终态，宽限期耗尽关闭连接。Adapter 无需另存一套 submitted/cancel 状态。该功能需要更新后的 SDK；仅使用本地 go.work 通过不能证明远端固定版本已发布。

`agent.go` 提供一次性显式 `SessionAdmission` 绑定。`NewAgentSideConnectionWithBinder` 在读帧前完成绑定。合法的 Prompt 和 Cancel 在读取时按帧序登记或标记 Adapter 的确切所有者，返回的执行在现有请求协程和通知队列中运行。接纳钩子只做短时同步操作；问答包装层可冻结准备函数，由获准执行的所有者用其准备上下文运行，并在释放执行前完成清理。已接管的空闲 Cancel 直接结束，已捕获的取消延续在入队失败或队列无法排空时仍执行一次。验证见 `session_admission_test.go`。

`connection.go` 优先交付已收到的完整响应，并继续检查该响应的先序通知水位。断线后通知交付失败仍返回错误，同时保留已收到的终态及用量。完整帧写入报错时，保留已发布到同一 pending 请求的响应和传输错误，立即收回尚未收到响应的 pending。验证见 `connection_terminal_test.go`。

`connection.go` and `client.go` share one request exchange between the existing request APIs and the opt-in client receipt. The reader stamps responses and notifications in one sequence. A construction-time ingress hook can fold bounded local notification state before dispatch. `SendClientRequestWithReceipt` separates the decoded result, peer RPC error, decode failure, response wait failure, and actual write receipt. `WaitForNotifications` waits separately for dispatch through the response's watermark. The legacy request APIs still wait for the same pre-response notifications and project errors as before. Count and byte limits continue to backpressure the reader when the SDK queue is full. Pipe tests in `connection_receipt_test.go` cover response ordering, interrupted dispatch, EOF, complete writes with transport errors, and malformed input.

Both request APIs validate response envelopes after the reader has identified the response's unambiguous ID. Matched malformed envelopes and result decode failures receive safe error categories, with no raw payload in the diagnostic. A valid peer RPC error retains its original code, message, and data for private classification. Legacy no-result requests continue to accept JSON null.
