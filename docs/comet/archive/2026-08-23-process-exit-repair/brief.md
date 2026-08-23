# Outcome

消除 Codex app-server 子进程退出与 stdout drain 并发时的错误归因竞态。任何已确认的异常退出都应稳定保留 `ErrAppServerExited`、退出码和有界 stderr；真实 reader I/O 错误、超长帧与主动关闭仍保持各自语义。修复严格限定在 process/transport fatal 仲裁，不改动已归档的 session、turn、approval 或 steering 状态机。

# Scope

- 直接对照固定 `.upstream/codex-acp` `ba5bcc3d7759250dde9d4d2286a1bec11b363208` 的 `CodexAcpServer.runWithProcessCheck` 与 `process-exit-error.test.ts`，保持进程 exit code/stderr 优先于并发 operation error 的行为。
- 定位并修复 Go `exec.Cmd.Wait`、`StdoutPipe` scanner 和 `process.FinalError` 的终止顺序/仲裁，使 closed-pipe 竞态不会绕过 `EOFError`。
- 保留 oversized frame、普通 scanner I/O error、clean EOF、主动 `Agent.Close` 的原有错误和无死锁清理语义。
- 先增加确定性的 closed-pipe/process-exit RED，再保留真实 helper process 的高并发压力回归。
- 更新 `UPSTREAM.md` 与必要中文注释，说明 Go 机制差异、优先级和为什么不能把所有 scanner error 统一改写为进程退出。

# Non-goals

- 不修改 ACP SDK connection/dispatch/cancel/extension 边界。
- 不重写 Codex NDJSON transport，不新增协议 DTO、进程抽象或假想扩展接口。
- 不修改 session、prompt、turn、approval、event、steering、config 或 auth 业务语义。
- 不通过放宽测试断言、增加 sleep、串行化全仓测试或延长生产 timeout 掩盖竞态。
- 不 push、不创建 PR、不发布制品。

# Acceptance examples

- A1：当 Codex app-server 以非零状态退出且 stdout 同时关闭时，所有 pending 请求稳定返回可由 `errors.Is(..., ErrAppServerExited)` 识别的错误，并保留退出码与有界 stderr。
- A2：超长帧、普通 reader I/O error、clean EOF 与主动关闭仍保留原有优先级；主动关闭不会等待自身或造成 goroutine/process 泄漏。
- A3：确定性 fatal 仲裁测试与真实进程 `TestAgentSurfacesProcessExitToPendingInitialize` 并行压力重复通过，不依赖长 sleep。
- A4：默认 `go test ./...`、默认 `go test -race ./...`、`go vet ./...`、protocol freshness、gofmt/diff、native/Linux/Windows 构建通过。
- A5：修复可追溯到固定 upstream，手写 Go 声明/字段/函数与关键竞态逻辑有清晰中文注释，且未引入重复 SDK/JSON-RPC/DTO 或无消费方抽象。

# Constraints and invariants

- 行为优先级：父变更已确认 Spec > 固定 codex-acp 源码/fixture > Go 工程偏好。
- `ErrFrameTooLarge` 必须优先于进程退出；仅可确认的 process-exit/closed-pipe 竞争允许切换到 `process.FinalError`，真正的 scanner I/O error 不得被吞掉。
- process、pipe、scanner、pending call 和 fatal 广播必须有单一清晰所有权；清理有界且幂等。
- 生产实现和测试均采用 TDD 与显式同步点；禁止依赖调度概率作为正确性证据。
- 所有改动只提交到本地 Git。

# Decisions

- 2026-08-24：父级最终 Verifier 在关键组合压力 `count=20 -parallel=16` 中两次复现错误根因丢失；隔离并行 `count=100` 两次失败，串行 `count=100` 全过，确认是调度竞态而非断言波动。
- 2026-08-24：父级失败以 A3/A17 路由到本 repair child；细分影响为 runtime process-exit 稳定错误与 verification child-process-exit 发布门槛。
- 2026-08-24：最小范围只允许修改 `internal/codex/process.go`、`internal/codex/appserver_transport.go`、对应测试与 `UPSTREAM.md`；若源码证据证明可进一步缩小，应采用更小范围。

# Open questions

- 无。

# Verification expectations

- RED：构造 scanner 返回 `os.ErrClosed` 且 process final error 已可用的确定性顺序，证明旧实现错误广播 generic read error。
- GREEN：定向测试 normal/race 多轮；真实 helper process 隔离并行至少 `count=100 -parallel=16`；关键 runtime 组合压力至少 `count=20 -parallel=16`。
- 发布门槛：严格串行运行默认全仓 test、默认全仓 race、vet、protocol freshness、gofmt/diff、native/Linux amd64/Windows amd64 build。
- 独立 Verifier 逐项验 A1-A5，并向父变更提供 A3/A17 修复证据。
