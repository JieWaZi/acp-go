# Outcome

在不新增协议框架或业务状态机的前提下，把已完成的 framework、runtime、events、approval、config 与 auth 组件接入同一个生产 `codex.Agent` 和 `acp-agent` composition root，并用可控 fake Codex app-server 形成 V1 关键路径的重复验证证据。最终代码在 macOS/Linux 发布门槛、Windows best-effort 编译、中文注释和 upstream 可追溯性方面闭环。

# Scope

- 直接复用现有 `acp-go-sdk` connection/dispatch/cancel/extension API，验证外层 ACP stdio 到内层 Codex app-server NDJSON 的组合链路。
- 把已有 `authenticator` 接入 `Agent.Initialize`、`Agent.Authenticate` 与 `Agent.Logout`；initialize 只声明 ChatGPT/API Key 两种实际可用认证方式。
- 把已有 `sessionConfiguration` 接入 new/load/resume、prompt/turn、`SetSessionMode` 与 `SetSessionConfigOption`；配置与模式更新按固定 codex-acp 的顺序、校验和安全策略传递，不重写 config 组件。
- 把已有 event router、approval handler 和历史 mapper 接入生产 session/turn generation；load 的已有历史按固定 upstream 行为转换为 ACP update，stale 身份仍 fail closed。
- 增加真实进程形状的可控 fake app-server 集成：至少覆盖 initialize、认证/模型目录、thread start/resume/read、turn stream/completion、server approval、cancel/interrupt、stderr/exit 与资源释放中的关键路径。
- 建立 A1-A18 的验证映射和本地发布检查：format、protocol freshness、unit、race、vet、macOS/Linux build，Windows best-effort cross build；真实凭据 smoke 只能报告为 run/pass 或 not-run，不得伪造。
- 审计所有手写 Go 声明、结构体字段、函数及关键并发/异常/映射逻辑的中文注释；生成代码豁免。
- 更新根 `UPSTREAM.md` 和测试映射，使新接线能定位固定 `codex-acp` 的源码 symbol/fixture；只记录必要 Go 机制差异和 V1 裁剪。

# Non-goals

- 不优化或替换已通过 freshness/typed-envelope 验收的 protocol generator。
- 不实现 Goal、Review、Gateway Auth、AIR、全部 Slash、Audio、client-provided MCP server、完整特殊事件集或第二个 Adapter。
- 不新增自有 ACP JSON-RPC、connection、dispatch、cancel、extension 或重复协议 DTO。
- 不重新设计已完成的 event、approval、config、auth、session、prompt、steering 状态机；接线中发现缺陷时先对照固定 upstream，再做最小等价修复。
- 不要求真实 Codex 凭据，也不 push、不建 PR、不发布制品。

# Acceptance examples

- A1：通过 `acp-go-sdk` stdio connection 启动 `acp-agent` 与可控 fake app-server，initialize 返回与 V1 实现一致的 prompt/session/auth 能力，且协议 stdout 无日志或子进程内容污染。
- A2：ChatGPT 与 API Key 认证入口复用现有 authenticator；API Key 不进入日志、错误或测试快照，ChatGPT 完成通知/取消/失败按固定 upstream 行为结束。
- A3：new/load/resume 返回并维护实际 session mode/config options；model、reasoning effort、mode 的有效更新进入后续 turn，非法或 stale session 更新保持原状态并返回稳定错误。
- A4：load 按 `thread/resume -> thread/read(includeTurns=true)` 顺序恢复配置和必要历史；历史消息/工具内容通过现有 mapper 发送，重复 completed 不产生重复最终 update。
- A5：生产通知与 server approval 路由使用精确 thread/turn/session generation；fake app-server 关键链路覆盖流式消息、工具/审批、completion、cancel/interrupt、stderr/exit 和 close，旧事件/审批不作用于新 turn。
- A6：`go test ./...`、`go test -race ./...`、`go vet ./...`、protocol freshness、macOS native build 和 Linux build通过；Windows cross build作为 best-effort 证据记录；所有命令带有可重复的边界条件。
- A7：A1-A18 每项具有自动化、静态审阅或明确环境证据；真实凭据 smoke 未运行时标为 not-run 并说明前置条件。
- A8：所有手写 Go 类型、字段和函数有清晰中文注释；关键状态转换、并发、异常兜底和复杂映射解释“为什么”并定位固定 upstream；生成文件只检查生成标记和 freshness。
- A9：最终差异审查未发现重复 acp-go-sdk 协议能力、重复 Codex 协议 DTO、无消费方预设抽象或脱离固定 codex-acp 的自创业务语义。

# Constraints and invariants

- 行为优先级与父变更一致：确认的 V1 Spec > 固定 `codex-acp` `ba5bcc3d7759250dde9d4d2286a1bec11b363208` > Go 工程偏好。
- 开发和测试前逐 symbol 读取 `.upstream/codex-acp` 对应源码和 fixture；能直接等价移植的行为必须直接移植，只有 Go 生命周期/并发机制和明确 V1 裁剪允许薄适配。
- ACP 协议边界继续直接使用 `github.com/coder/acp-go-sdk` v0.13.5；禁止另写 JSON-RPC connection/dispatch/cancel/extension。
- 复用已生成 protocol 类型和集中 method 常量；运行时代码不得手写重复 app-server DTO。
- approval 异常、handler 缺失、取消或 stale 永远 fail closed；generation 检查在外部 callback 前后都有效。
- 配置更新必须按 session 隔离并参与后续 turn；session close 解除配置、审批、steering、事件和 pending waiter 所有权。
- goroutine、channel、process、pending call/observer 有明确所有者、终止条件和有界策略；测试使用确定性屏障，不用长 sleep 证明正确性。
- 所有手写 Go 声明/字段/函数及关键复杂逻辑使用中文注释；简单自明语句不堆叠逐行注释。
- 只做本地开发、完整验证与本地 Git；不配置 remote、不 push、不创建 PR。

# Decisions

- 2026-08-24：父变更已完成并独立验收 framework-foundation、protocol-generation、codex-runtime、codex-events-config；本子变更只做生产接线、端到端验证与发布审计。
- 2026-08-24：用户再次强调 Upstream First：固定 codex-acp 已有源码/fixture 能直接复用时必须等价移植，不得自行创造实现；能力裁剪和 Go 机制差异需进入 `UPSTREAM.md`。
- 2026-08-24：用户要求优先完成主要功能，因此 protocol generator 优化留给后续，本阶段只执行既定 freshness 门槛。
- 2026-08-24：采用显式 composition root 与手工构造注入；复用现有小接口，不引入 DI 容器或新的架构层。
- 2026-08-24：真实凭据 Codex smoke 是可选环境证据；可控 fake app-server 的关键路径集成是发布硬门槛。

# Open questions

- 无。

# Verification expectations

- TDD：先用真实生产构造/SDK stdio/fake app-server 暴露缺失接线，再做最薄 GREEN；任何修复先定位固定 upstream symbol/test。
- 单元与集成：覆盖认证、配置、历史恢复、事件、审批、cancel、stale、process stderr/exit 和资源释放；重复/竞态路径使用确定性同步点。
- 工程：`gofmt`/diff check、`go run ./tools/protocolgen --check`、`go test ./...`、`go test -race ./...`、`go vet ./...`、native/Linux build，Windows best-effort cross build。
- 静态审阅：检查手写 Go 中文注释、UPSTREAM 映射、SDK 直接复用、generated DTO 边界和无用抽象；生成代码不做中文审计。
- 最终 verification report 逐项记录 A1-A9，并汇总父变更 A1-A18 的可重复证据与真实凭据 smoke 状态。
