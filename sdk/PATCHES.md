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

`connection.go` 与 `client.go` 共用一个请求交换和 pending 表。写门检查 context 并通过 Commit 后、调用 Writer 前才登记可匹配的 ID；reader 在解析前读取写门接纳范围，已经观察到的早到帧不能因解析期间的 Commit 而命中。真实快速响应可以早于 Writer 返回。reader 领取并移除匹配的 pending 后，在连接锁外校验、捕获并发布；若等待取消或写入报错，已领取的响应仍会交付。没有收到响应的等待方可以退役自己的 pending，无需等候下一帧。接纳范围表示本地写门状态，不证明对端实际消费。

`SendClientRequestWithReceipt` 独立返回结果、peer RPC error、解码错误、等待错误和实际写入回执。无钩子或显式传 nil 时，类型解码由调用方执行；只有显式非 nil 钩子才让类型解码与钩子在 reader 上执行一次，并在下一帧前完成。两者都限于有界本地操作，禁止 I/O、等待、应用观察或重入连接。通知钩子在入队前合并有界状态；`WaitForNotifications` 按收据水位独立等待分发，预算耗尽时仍由 reader 背压。旧请求入口保留先序通知屏障、写入错误与 peer 错误投影，以及合法 JSON null。可匹配的畸形响应和解码错误只提供安全分类；合法 peer RPC error 保留私有原始证据，适配器在公开前负责脱敏。

所有反向请求和通知均先按 method、ID 成员、版本和互斥字段分类校验。一个活跃 canonical ID 只有一个响应与取消所有者，捕获、普通处理与安全拒绝共用占位。业务处理中及回复等待写门时，重复活跃 ID 静默丢弃，避免第二个同 ID 响应完成第一个请求的等待方。回复取得共享写门并即将调用 Writer 时按身份退役，对端读完回复后可立即复用 ID，即使本地 Write 尚未返回。此处采用本地可证的写入起点，通用 Writer 不提供对端读帧时刻。前一代的退出清理仅核对自己的身份，后续回复也经同一写门保持顺序。协议取消只接受合法的通知信封与无歧义的目标 ID，在 reader 同步取消已占位的所有者。合法 `PrepareRequest` 可按接收序捕获所有者，延续仍在 reader 外执行；未捕获的方法走既有处理器。`SendClientNotificationWithWriteHooks` 与请求共用写门，可在发送原生取消前复核目标。

参考 OpenBot 固定提交 `f44dbe1` 的 `src/backend/acp-connection.ts:108–124` 和 `src/backend/acp-client.ts:2137–2204`，以及锁定的 TypeScript SDK 1.9.0 发布包 `dist/stream.js:23–56`、`dist/jsonrpc.js:49–89,548–574,659–671,734–742,799–860`。Go 沿用统一信封分类、显式 mapper 与按身份退役，在共享读写路径增加写门归属、拒绝重复活跃 ID，并保留字节预算和无载荷诊断。真实管道回归见 `connection_receipt_test.go`，覆盖等待写门、Commit 拒绝、快速响应、写错／取消竞争、阻塞用户解码、重复 ID、应答写入期间合法复用 ID、旧清理与新代取消，以及非法协议取消。
