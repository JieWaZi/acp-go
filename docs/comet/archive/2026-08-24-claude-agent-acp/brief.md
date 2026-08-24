# Outcome

在不改变现有 Codex Adapter 默认行为的前提下，为 `acp-go` 增加显式选择的 Claude Adapter。ACP 客户端能够通过用户预装且已登录的 Claude Code CLI 创建、加载、恢复和关闭会话，进行多轮 prompt、steering 与 cancel，并接收 Claude 的文本、思考、usage、计划、工具调用和权限更新。

V1 固定 `agentclientprotocol/claude-agent-acp` `v0.70.0`（commit `d0aafb1ca26427285ffaeac8d8a4452fff28e9c3`）及其 `@anthropic-ai/claude-agent-sdk` `0.3.232` 依赖作为行为/协议基线。实现使用原生 Go 直接驱动 Claude CLI 的 stream-json/control 协议，不引入 Node sidecar。

# Scope

## Adapter 与进程边界

- `cmd/acp-agent` 注册 `claude` Adapter；无参数和 `--adapter codex` 仍选择 Codex，`--adapter claude` 才构造 Claude。
- 复用现有 `internal/core.Registry`、`internal/acpserver`、acp-go-sdk connection 和 extension dispatch；Claude 代码进入独立 `internal/claude` 与 `agents/claude/protocol` 包。
- Claude CLI 由用户预装；`CLAUDE_CODE_EXECUTABLE` 非空时只使用该路径，否则从 PATH 查找 `claude`。不下载、不捆绑、不通过 Node 启动。
- 每个活动 ACP Session 拥有一个独立、持续运行的 Claude CLI 进程、输入队列、control request 表和消费循环；Adapter Close 统一释放所有 Session。

## V1 功能

- initialize 只声明 V1 实际实现的 image、embedded context、load/resume/close、additional directories、client MCP 与 steering 能力。
- new/load/resume/close Session；load 回放可表达的历史消息，resume 只恢复会话而不回放历史。
- prompt FIFO、多轮消息、`_session/steering`、session cancel、进程异常退出与关闭清理。
- Text、Image、Resource/embedded context 输入。
- assistant text、thinking、usage、基础 Task/Todo plan 更新。
- Claude 内置工具与 MCP 工具的开始、进度和完成映射。
- `can_use_tool` control request 到 ACP permission 的 fail-closed 映射。
- model、effort、fast、permission mode 配置；通过 control request 在活动 Session 上更新。
- stdio、HTTP、SSE 三类客户端 MCP server 和 additional directories 到 Claude CLI 启动参数的映射。
- 固定 upstream 源文件、Agent SDK 类型/协议声明和代表性 fixtures 的追踪记录。

## 实施顺序

1. 固定 upstream 证据，建立窄类型 Claude protocol package 与 fake CLI fixture harness。
2. 实现可执行文件发现、参数构造、JSONL/control transport、进程退出和幂等关闭。
3. 实现 Session store、new/load/resume/close、prompt consumer、cancel 与 steering 状态机。
4. 实现输入、消息/思考/usage/plan、工具调用和权限映射。
5. 实现 model/effort/fast/mode、additional directories、client MCP 及 capability 声明。
6. 接入 composition root，补齐文档、upstream 映射、全量/竞态/构建验证与 Codex 回归检查。

# Non-goals

- 不改变 Codex Adapter 的协议、进程模型、默认选择或既有可观察行为。
- 不实现 upstream 完整等价范围中的 session list/fork/delete、providers、goal、结构化 session failure、文件变更审计、嵌套 subagent 富展示、slash command 列表、ACP terminal、elicitation、认证/登出或 post-v0.70 permission extension/clear-context planning。
- V1 不提供 Claude 登录流程；使用本机 Claude CLI 的既有认证状态，认证缺失按 prompt/runtime 错误返回。
- 不直接复制 upstream TypeScript 单体或提交 Anthropic Agent SDK 的压缩产物。
- 不建立覆盖 Codex/Claude 全部能力的通用 Runtime 大接口，也不在两种进程协议尚未证明同构前提取共享 transport。
- 不由本变更推进、提交或收尾 `acp-multi-agent-codex-go-v1`。
- 不把真实 Claude 账号、网络或付费调用作为默认测试前置条件。

# Acceptance examples

- A1：运行 `acp-agent` 或 `acp-agent --adapter codex` 的选择和 Codex 行为保持不变；`acp-agent --adapter claude` 只构造 Claude Adapter；未知名称仍在写 ACP stdout 前失败。
- A2：Claude initialize 返回名称/版本以及与 V1 一致的 image、embedded context、load/resume/close、additional directories、MCP HTTP/SSE 和 steering 声明；不宣告 auth、list/fork/delete/providers/goal/file-report/elicitation/terminal 等未实现能力。
- A3：Claude 可执行文件严格按 `CLAUDE_CODE_EXECUTABLE` → PATH `claude` 解析；显式路径无效时不回退，缺失时返回可诊断错误；探测到的版本只写 stderr 诊断，不污染 ACP stdout。
- A4：每个 Session 使用独立 Claude CLI，启动参数包含 `--output-format stream-json --verbose --input-format stream-json` 及所需配置；stdout JSONL、stderr、stdin 写入、control request/response/cancel 和进程退出均由有界 transport 管理。
- A5：非 JSON、未知消息和未知字段按固定基线记录安全摘要并兼容处理；超限帧、写失败、非零退出和信号退出产生稳定错误，保留有限 stderr 尾部且不泄露敏感配置。
- A6：new session 使用请求 cwd、additional directories、MCP 和初始配置启动进程，完成 control initialize 并取得稳定 Claude Session ID；并发 Session 的进程、请求和事件不会串线。
- A7：resume 使用已有 Session ID 恢复但不回放历史；load 在恢复后按 upstream v0.70.0 语义回放可表达的 user/assistant/tool 历史并过滤本地命令标记；参数变化不会复用错误的活动进程。
- A8：同一 Session 的 prompt 按 FIFO 关联用户消息 echo、stream/result/idle，连续多轮各自只完成一次；流先结束、结果缺失或 Session 已终止时请求不会永久悬挂。
- A9：`_session/steering` 在活动 turn 中以 priority 消息注入，在空闲时按 upstream 默认启动 detached turn；cancel 中断活动 query、清理排队 turn，并以 cancelled 语义解除等待；迟到 echo/result 不得完成后续 turn。
- A10：ACP Text、Image、Resource/embedded context 被严格转换为 Claude user message；保留 MIME、data/URI 和上下文文本，不支持的 Audio/内容类型返回明确错误而非静默丢弃。
- A11：流式与组装后的 assistant text/thinking 不重复输出；result/usage 映射为准确的 prompt stop reason 与 usage update；Task/Todo 状态映射为基础 ACP plan。
- A12：Claude 内置工具与客户端 MCP 工具使用稳定 ToolCallID 映射开始、进度、输出和完成；已完成工具不会被迟到进度重新打开，未知工具仍保留安全的 generic tool call 信息。
- A13：Claude `can_use_tool` control request 映射为 ACP `session/requestPermission`；选项和值严格校验后回传 control response，取消、连接缺失、过期 Session/turn、未知选择和请求失败全部 fail closed。
- A14：model、effort、fast 与 permission mode 选项来自 CLI control initialize/固定基线信息；设置变更通过对应 control request 生效，非法值和已关闭 query 返回可诊断错误且不扩大权限。
- A15：stdio、HTTP、SSE 客户端 MCP 配置以及 additional directories 被稳定映射到 Claude CLI 参数；名称、命令、URL、headers 和 env 保持请求语义，敏感 env/header 不进入日志。
- A16：Session close、Adapter close、ACP 断开、cancel 与进程自然/异常退出并发时清理幂等；所有等待 control/prompt/permission 的调用都会解除，goroutine、timer、pipe 和子进程不泄漏。
- A17：`UPSTREAM.md` 固定 claude-agent-acp v0.70.0/`d0aafb1…`、ACP TS SDK 1.3.0、Claude Agent SDK 0.3.232 和 acp-go-sdk v0.13.5，记录纳入/跳过模块、TS/SDK→Go 映射、协议来源、等价改写与升级步骤。
- A18：fake Claude CLI 与冻结 fixtures 在无账号/网络条件下覆盖 A1-A17 的自动化证据；`go test ./...`、`go test -race ./...`、`go vet ./...`、格式检查和目标平台构建通过，既有 Codex 测试无回归，手写 Go 声明及关键状态机保持清晰中文注释。

# Constraints and invariants

- 行为优先级：确认后的 target specs > 固定 claude-agent-acp v0.70.0/Agent SDK 0.3.232 行为 > Go 工程偏好。
- ACP stdout 只能承载协议消息；版本、CLI stderr、未知消息和错误诊断只进入 stderr logger。
- Claude 的 session-owned process 是设计事实，不能套用 Codex 的 process-wide shared app-server。
- 进程输入保持单写者或等价串行化；每个 control request 使用唯一 ID 和有界等待；未知载荷保留 `json.RawMessage`，不得因为新字段破坏已知消息。
- prompt、steering、cancel、close 和进程退出必须使用 Session generation/turn identity 或等价 fence 防止迟到事件串线。
- 权限异常始终 fail closed；日志不得包含 token、MCP secret header/env、完整敏感 prompt 或未截断 stderr。
- 队列、帧、stderr tail、并发请求和关闭等待全部有界；goroutine/channel/timer/process 有明确所有者和终止条件。
- 直接复用 acp-go-sdk 的 Agent、AgentLoader、AgentSideConnection 与 ExtensionMethodHandler，不实现第二套 ACP JSON-RPC。
- 新接口只在消费方定义并保持小型；构造函数返回具体 Claude 类型，不建立通用 Runtime 大接口。
- `.upstream/claude-agent-acp` 保持 Git 忽略；实现与测试必须对照固定 tag 的具体源文件、SDK public declarations 和 fixtures。
- Go 手写类型、每个字段定义、函数和函数内部关键/复杂逻辑使用中文注释；注释只陈述当前代码的职责、顺序、边界和失败语义，不写版本来源、对照实现或移植说明。协议字段保留 wire 原名，生成/fixture 数据豁免翻译。
- upstream 版本、symbol、fixture、实现映射和有意差异只记录在 `UPSTREAM.md`，不得散落在代码注释中。
- 本任务仅修改 `comet/claude-agent-acp` worktree 中属于 Claude 变更的文件，保留其他工作区和旧变更。

# Decisions

- 2026-08-24：使用 Comet Native，change 为 `claude-agent-acp`，分支为 `comet/claude-agent-acp`，isolation 为 linked worktree，target 为 `main`。
- 2026-08-24：用户选择 `1A`，固定最新稳定 `claude-agent-acp` `v0.70.0`/`d0aafb1…`，不追随 post-release `main@996d488`。
- 2026-08-24：用户选择 `2A`，原生 Go 启动用户预装 Claude CLI 并实现所需 stream-json/control 协议，不使用 Node sidecar。
- 2026-08-24：用户选择 `3A`，首期交付核心可用 V1；完整 upstream 等价能力留给后续独立变更。
- 2026-08-24：Codex 永久保持默认 Adapter，Claude 仅显式选择。
- 2026-08-24：Claude 使用 `CLAUDE_CODE_EXECUTABLE` 作为显式覆盖，否则 PATH 查找 `claude`；显式值错误不静默回退。
- 2026-08-24：V1 使用本机 Claude CLI 既有认证，不宣告或实现 ACP authenticate/logout。
- 2026-08-24：一个 ACP Session 对应一个 Claude CLI 进程；load 回放历史，resume 不回放。
- 2026-08-24：测试以 fake CLI 和冻结 fixture 为发布证据，真实 Claude CLI 仅是 opt-in smoke test。
- 2026-08-24：保留 Registry/acpserver 共享边界；不预先共享 Codex/Claude transport。
- 2026-08-24：用户补充注释规范：所有手写字段定义必须有中文注释；函数内部关键/复杂逻辑必须有中文说明；代码注释不得写对照来源，所有 upstream 追踪信息集中到 `UPSTREAM.md`。
- 2026-08-24：大需求拆分预检结论为单一 Native change。虽然 V1 跨协议、运行时、事件和配置，但这些阶段共享同一 Session/turn/control 状态机且严格串行依赖；在接口未稳定前拆成 Supervisor children 会增加协调和重复返工，当前也未授权并行 agent 实施。

# Open questions

- [blocking] CONFIRM — 是否确认以上 Outcome、V1 Scope/Non-goals、A1-A18、约束、既定决策和单一 change 的实施顺序，允许 Comet 结束 Shape 并进入实现阶段？

# Verification expectations

- 单元级：CLI 发现、参数构造、协议判别、输入/事件/tool/permission/config mapper 使用表驱动测试。
- 生命周期级：fake CLI 确定性覆盖 new/load/resume/close、连续 prompt、FIFO、steering、cancel、坏帧、写失败、进程退出和 ACP 断开；同步测试不得依赖无依据 sleep。
- 并发级：运行 `go test -race ./...`，重点检查 per-session process、pending control map、turn queue、permission、close/cancel/exit 竞态。
- 追踪级：冻结 upstream v0.70.0 与 Agent SDK 0.3.232 的代表性消息/fixture，并用 `UPSTREAM.md` 证明 A2-A17 的来源或明确 Go 等价改写。
- 工程级：`gofmt`、`go test ./...`、`go test -race ./...`、`go vet ./...` 和仓库支持平台构建通过；Codex 现有测试必须继续通过。
- 集成级：真实 Claude CLI smoke test 仅在显式环境开关且用户已有认证时运行；未运行必须如实记录，不能用 fake/build 结果冒充真实调用验证。
- 审阅级：逐项将 A1-A18 关联到自动化测试或明确的 opt-in 证据，并检查能力声明、日志脱敏、中文注释和 Non-goals 边界。
