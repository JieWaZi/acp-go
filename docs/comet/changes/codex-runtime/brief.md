# Outcome

交付可运行的 Codex app-server 生命周期、session/turn、prompt/cancel 与 FIFO steering 主链路，并直接复用 acp-go-sdk 承载外层 ACP 协议。

# Scope

- 按 `CODEX_PATH` → PATH 解析用户预装 Codex，校验 0.148.0 基线并启动唯一 `codex app-server`。
- 实现 Codex 特有的无 `jsonrpc` 字段 NDJSON transport、initialize 与生成 DTO typed client。
- 实现 thread start/resume/read、session generation/close fence、prompt、多轮 turn、early/stale completion。
- 实现 cancel/late turn start/interrupt 与每 session 有界 FIFO steering。
- 在 foundation Agent 上完成并发安全 connection binder、幂等关闭和 SDK integration。

# Non-goals

- 不重写外层 ACP JSON-RPC、dispatch、cancel 或 extension，直接使用 acp-go-sdk v0.13.5。
- 不实现完整事件/tool/approval/config/auth 映射，由兄弟变更 codex-events-config 负责。
- 不引入 HTTP、WebSocket、Unix Socket、自动下载 Codex 或进程自动重启。

# Acceptance examples

- A1：显式无效 `CODEX_PATH` 不回退，PATH 缺失返回诊断；非 0.148.0 只警告并继续，stdout 不受污染。
- A2：ACP initialize 只触发一次 app-server initialize，随后 new/resume/load 可建立隔离 session。
- A3：同一 session 连续多轮 prompt；early completion、旧 turn completion 与跨 session 事件不会错误结束当前 prompt。
- A4：cancel-before-start 不留下 turn；迟到 turn/start 被标记 stale 并 interrupt；重复取消幂等。
- A5：并发 steering 严格 FIFO，无活动 turn 时启动新 turn，单项失败后队列继续且不产生 rival turn。
- A6：app-server EOF/exit、session close 与 Adapter close 会解除 pending request/waiter/queue，无 goroutine 泄漏。
- A7：`go test ./...`、`go test -race ./...`、`go vet ./...` 与目标构建通过。
- A8：全部手写 Go 类型、字段和函数有中文注释，关键并发逻辑注明固定 codex-acp symbol/test 来源。

# Constraints and invariants

- 本地 `.upstream/codex-acp` 必须固定 HEAD `ba5bcc3d7759250dde9d4d2286a1bec11b363208`，实现直接对照源码、symbol 和 fixture。
- app-server params/result 只使用 `agents/codex/protocol` 生成类型；内部对象 union 严格按 discriminator 读取。
- 一个 Adapter 实例只管理一个 app-server 子进程；一个 session 同时最多一个 active/pending turn start。
- 不在持锁状态调用进程 IO、ACP callback 或等待网络结果；所有队列有界，所有 goroutine 有 context/owner。
- SDK `SetLogger` 在 v0.13.5 存在竞态，禁止调用。

# Decisions

- 保持与 `CodexJsonRpcConnection.ts`、`CodexAppServerClient.ts`、`CodexAcpServer.ts`、`SteeringQueue.ts` 可识别的文件/职责映射。
- Codex 内层 transport 必须自建薄边界，因为固定上游 wire 不带 `jsonrpc`；外层 ACP transport 必须复用 SDK。
- 使用具体类型和消费方小接口，不建立统一大 Runtime 接口或 DI 容器。
- 用户补充确认：固定 codex-acp 已有的状态机、异常兜底和测试必须直接等价移植；能由 acp-go-sdk 复用的能力直接调用。只有 Go 语言边界或明确 V1 裁剪允许差异，并须记录到 UPSTREAM.md 与关键中文注释。

# Open questions

- 无；父 Shape 与用户“优先主要功能”决定已授权本子变更。

# Verification expectations

- 严格 TDD，使用 channel/barrier 覆盖竞态，不用长 sleep 证明正确性。
- fake app-server 覆盖乱序 response、notification-before-response、server request、malformed、EOF/exit。
- 移植固定上游 session-close、early completion、cancel、steering 的核心测试语义。
