# Outcome

在本地模块 `acp-go` 中建立可编译、可测试的 ACP Agent-neutral 框架骨架：直接使用 acp-go-sdk v0.13.5 实现 Agent 侧协议边界，提供轻量 Adapter 注册/选择、默认 Codex 选择和显式 composition root，为后续 Codex runtime/event 子变更提供稳定而不过度抽象的接入点。

# Scope

- 引入并固定 `github.com/coder/acp-go-sdk` v0.13.5。
- 建立 `internal/core`（或等价）的小型 Adapter factory/registry/selector 与必要可选能力接口。
- 建立实现 `acp.Agent`/`acp.AgentLoader` 所需方法的协议入口骨架，并直接组合 `acp.NewAgentSideConnection`。
- 建立 `cmd/acp-agent`：默认 `codex`，接受 `--adapter codex`，未知 Adapter 在输出协议前失败。
- 使用显式构造和手工依赖注入；所有手写类型、字段和函数使用清晰中文注释。
- 只在框架测试中使用最小 fake Adapter，不实现 Codex app-server 业务。

# Non-goals

- 不实现 Codex 子进程、thread/turn、事件、审批、配置或协议生成。
- 不复制 acp-go-sdk 的 connection、dispatch、cancel、extension 实现。
- 不引入 DI 容器、全局注册、完整 Clean Architecture/DDD 或假想统一 Runtime。

# Acceptance examples

- A1：运行 `acp-agent` 或 `acp-agent --adapter codex` 都能完成组合根选择并进入由 acp-go-sdk 承载的 ACP stdio 服务；未知 Adapter 在写协议前返回可诊断错误。
- A2：Core 能注册并选择 Adapter，公开类型不包含 Codex Thread、Turn、Sandbox 或 Approval；可选能力由小接口按需发现，构造函数返回具体类型。
- A3：`go test ./...`、`go vet ./...` 和目标二进制构建通过，框架选择、重复注册、未知选择和 SDK 边界有自动化测试。
- A4：本子变更新增的全部手写 Go 类型、结构体字段和函数有清晰中文注释，关键选择/失败逻辑说明原因。

# Constraints and invariants

- 继承父变更 `acp-multi-agent-codex-go-v1` 的已确认 brief 与 `specs/framework/spec.md`。
- module path 保持 `acp-go`；只做本地 Git，不配置 remote、push 或 PR。
- 接口在消费方定义并保持小型；没有第二实现或测试边界时不抽接口。
- stdout 只保留 ACP JSON-RPC；诊断写 stderr。

# Decisions

- 2026-08-23：父 Shape 已授权本子变更，不重复请求用户确认。
- 2026-08-23：使用轻量 Ports-and-Adapters、显式构造和手工依赖注入。
- 2026-08-23：已在本工作树 `.upstream/` 克隆并固定 `codex-acp` `ba5bcc3d7759250dde9d4d2286a1bec11b363208` 与 `acp-go-sdk` `0845a3bb9eddda5bfc22a94dd3598c90cb842451`；框架启动顺序直接对照 `codex-acp/src/index.ts`，协议连接直接对照 SDK `agent.go` 与 `example/agent/main.go`，不凭空实现 JSON-RPC。
- 2026-08-23：固定的 acp-go-sdk v0.13.5 在 `NewConnection` 启动 receive goroutine 后调用 `SetLogger` 会触发 logger 无同步读写竞态；本项目不调用该可选 setter，保留 SDK 默认 stderr logger，stdout 仍只承载 ACP 帧；composition root 自有 logger 显式注入 Codex Agent，等待后续 runtime 使用。

# Open questions

- 无。

# Verification expectations

- 运行格式化、`go test ./...`、`go vet ./...`、`go build ./cmd/acp-agent`。
- 审查不存在重复 ACP JSON-RPC 实现、隐式全局注册或 Codex 专属类型泄漏。
- 审查手写声明的中文注释覆盖。
