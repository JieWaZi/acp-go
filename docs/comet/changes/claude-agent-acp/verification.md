---
generated_from_state_version: 13
---

# Verification

## Current result

- Result: **Passed**
- Assurance: **skill-coordinated**
- Goal cycle: 2
- Iteration: 2
- Verifier attempt: 1
- Completed: 2026-08-24T04:03:31.909Z
- Summary: 修复候选满足 A1-A98：Claude V1 功能、并发生命周期、安全边界、集中 upstream 追踪、中文注释和工程验证均通过；未发现阻断项。

## Acceptance

| ID | Result | Source | Criterion | Reason |
| --- | --- | --- | --- | --- |
| A1 | passed | brief.md | A1：运行 `acp-agent` 或 `acp-agent --adapter codex` 的选择和 Codex 行为保持不变；`acp-agent --adapter claude` 只构造 Claude Adapter；未知名称仍在写 ACP stdout 前失败。 | 组合根测试证明 Codex 仍为默认值，显式 Claude 才构造对应 Adapter，坏选择不会污染 ACP stdout。 |
| A2 | passed | brief.md | A2：Claude initialize 返回名称/版本以及与 V1 一致的 image、embedded context、load/resume/close、additional directories、MCP HTTP/SSE 和 steering 声明；不宣告 auth、list/fork/delete/providers/goal/file-report/elicitation/terminal 等未实现能力。 | Initialize 只声明已实现的 load、resume、close、内容、目录、MCP 与 steering 能力，未宣告裁剪能力。 |
| A3 | passed | brief.md | A3：Claude 可执行文件严格按 `CLAUDE_CODE_EXECUTABLE` → PATH `claude` 解析；显式路径无效时不回退，缺失时返回可诊断错误；探测到的版本只写 stderr 诊断，不污染 ACP stdout。 | 可执行文件解析严格执行显式路径优先且不回退，版本探测有界并只进入诊断日志。 |
| A4 | passed | brief.md | A4：每个 Session 使用独立 Claude CLI，启动参数包含 `--output-format stream-json --verbose --input-format stream-json` 及所需配置；stdout JSONL、stderr、stdin 写入、control request/response/cancel 和进程退出均由有界 transport 管理。 | 每个 Session 独占 CLI 进程，JSONL/control transport 的读写、pending 与并发均有明确上限。 |
| A5 | passed | brief.md | A5：非 JSON、未知消息和未知字段按固定基线记录安全摘要并兼容处理；超限帧、写失败、非零退出和信号退出产生稳定错误，保留有限 stderr 尾部且不泄露敏感配置。 | 坏帧、超限帧、EOF、退出、写失败和 stderr 尾部均有稳定、有界且脱敏的处理。 |
| A6 | passed | brief.md | A6：new session 使用请求 cwd、additional directories、MCP 和初始配置启动进程，完成 control initialize 并取得稳定 Claude Session ID；并发 Session 的进程、请求和事件不会串线。 | Session 在 control 与 system/init 双握手成功并核对 ID 后才安装，失败状态不会发布。 |
| A7 | passed | brief.md | A7：resume 使用已有 Session ID 恢复但不回放历史；load 在恢复后按 upstream v0.70.0 语义回放可表达的 user/assistant/tool 历史并过滤本地命令标记；参数变化不会复用错误的活动进程。 | resume 不回放；load 安全定位 transcript，并按顺序回放 user、assistant、image 与 tool 内容。 |
| A8 | passed | brief.md | A8：同一 Session 的 prompt 按 FIFO 关联用户消息 echo、stream/result/idle，连续多轮各自只完成一次；流先结束、结果缺失或 Session 已终止时请求不会永久悬挂。 | 单 worker、有界 channel、echo 身份、result/idle 和 settle/drain one-shot 共同保证 FIFO 与唯一完成。 |
| A9 | passed | brief.md | A9：`_session/steering` 在活动 turn 中以 priority 消息注入，在空闲时按 upstream 默认启动 detached turn；cancel 中断活动 query、清理排队 turn，并以 cancelled 语义解除等待；迟到 echo/result 不得完成后续 turn。 | steering、cancel epoch、队列线性化、interrupt 硬超时和迟到帧清理满足取消与转向语义。 |
| A10 | passed | brief.md | A10：ACP Text、Image、Resource/embedded context 被严格转换为 Claude user message；保留 MIME、data/URI 和上下文文本，不支持的 Audio/内容类型返回明确错误而非静默丢弃。 | Text、Image、ResourceLink、embedded resource 均严格转换，Audio 与坏 union 明确拒绝。 |
| A11 | passed | brief.md | A11：流式与组装后的 assistant text/thinking 不重复输出；result/usage 映射为准确的 prompt stop reason 与 usage update；Task/Todo 状态映射为基础 ACP plan。 | stream/assembled 前缀去重、usage、stop reason 与 Task plan 映射均已实现并由 fake CLI 覆盖。 |
| A12 | passed | brief.md | A12：Claude 内置工具与客户端 MCP 工具使用稳定 ToolCallID 映射开始、进度、输出和完成；已完成工具不会被迟到进度重新打开，未知工具仍保留安全的 generic tool call 信息。 | 已知、MCP 与未知工具均形成稳定 start/progress/result 生命周期，完成态不会被迟到进度重开。 |
| A13 | passed | brief.md | A13：Claude `can_use_tool` control request 映射为 ACP `session/requestPermission`；选项和值严格校验后回传 control response，取消、连接缺失、过期 Session/turn、未知选择和请求失败全部 fail closed。 | can_use_tool 选项、permission suggestions、客户端返回与 stale turn 均严格校验并 fail closed。 |
| A14 | passed | brief.md | A14：model、effort、fast 与 permission mode 选项来自 CLI control initialize/固定基线信息；设置变更通过对应 control request 生效，非法值和已关闭 query 返回可诊断错误且不扩大权限。 | model、effort、fast 与 mode 按 Session 串行更新，非法值和失败不会扩大权限。 |
| A15 | passed | brief.md | A15：stdio、HTTP、SSE 客户端 MCP 配置以及 additional directories 被稳定映射到 Claude CLI 参数；名称、命令、URL、headers 和 env 保持请求语义，敏感 env/header 不进入日志。 | additional directories 与 stdio/HTTP/SSE MCP 在启动前校验并稳定编码，secret 不进入诊断。 |
| A16 | passed | brief.md | A16：Session close、Adapter close、ACP 断开、cancel 与进程自然/异常退出并发时清理幂等；所有等待 control/prompt/permission 的调用都会解除，goroutine、timer、pipe 和子进程不泄漏。 | Session/Adapter close、transport fatal、cancel 超时与进程退出均会解除等待并在硬期限内回收。 |
| A17 | passed | brief.md | A17：`UPSTREAM.md` 固定 claude-agent-acp v0.70.0/`d0aafb1…`、ACP TS SDK 1.3.0、Claude Agent SDK 0.3.232 和 acp-go-sdk v0.13.5，记录纳入/跳过模块、TS/SDK→Go 映射、协议来源、等价改写与升级步骤。 | UPSTREAM.md 集中记录固定版本、symbol/文件映射、fixture、差异和升级步骤。 |
| A18 | passed | brief.md | A18：fake Claude CLI 与冻结 fixtures 在无账号/网络条件下覆盖 A1-A17 的自动化证据；`go test ./...`、`go test -race ./...`、`go vet ./...`、格式检查和目标平台构建通过，既有 Codex 测试无回归，手写 Go 声明及关键状态机保持清晰中文注释。 | fake CLI、冻结 fixture、全量测试、race、vet、格式、注释审计和三平台构建均通过。 |
| A19 | passed | specs/claude-events-config/spec.md | initialize 只声明 V1 已实现的 image、embedded context、load/resume/close、additional directories、MCP HTTP/SSE 和 `_session/steering`；agent name/title/version 稳定且可测试。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A20 | passed | specs/claude-events-config/spec.md | V1 不宣告 auth/logout、session list/fork/delete、providers、goal、file report、typed failure、terminal、elicitation 或 post-v0.70 扩展。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A21 | passed | specs/claude-events-config/spec.md | ACP Text 转为 Claude text content；Image 保留 MIME 与 data/URI；Resource/embedded context 转为带来源标识的文本/文档上下文。Audio 和未知 content block 返回 invalid request，不静默丢弃。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A22 | passed | specs/claude-events-config/spec.md | top-level assistant text 映射为 ACP agent message chunk，thinking 映射为 thought chunk；stream delta 与 assembled message 做前缀/身份去重，不能重复最终内容。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A23 | passed | specs/claude-events-config/spec.md | result 的成功、错误、取消与上下文/预算类 stop reason 转为稳定 ACP PromptResponse；usage 使用当前 turn/会话的明确口径并携带可用的 context window 信息。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A24 | passed | specs/claude-events-config/spec.md | Task/Todo 创建、更新与完成维护 per-session task snapshot，并发布基础 ACP plan；V1 不实现 goal extension 或嵌套 subagent transcript。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A25 | passed | specs/claude-events-config/spec.md | 未知事件、未知 content block 与不属于当前 Session/turn 的事件记录有界安全摘要后忽略；核心 V1 事件不能落入未知分支而静默丢失。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A26 | passed | specs/claude-events-config/spec.md | Claude tool_use 建立稳定 ToolCallID；Read/Edit/Write/Bash/Search/Glob/Task/Todo 等已知工具映射合适标题、kind、位置、原始输入和状态。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A27 | passed | specs/claude-events-config/spec.md | tool_progress 只更新已经发布且仍活动的 ToolCall；tool_result 完成对应调用并保留可表达的文本、图片、diff/路径和错误状态。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A28 | passed | specs/claude-events-config/spec.md | MCP tool 名称与输入/输出作为 generic MCP ToolCall 映射；未知工具仍发布安全 generic ToolCall，不因 mapper 缺项终止 prompt。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A29 | passed | specs/claude-events-config/spec.md | tool cache 按 turn/session identity 清理；迟到进度或结果不得重新打开已完成调用或串到另一 Session。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A30 | passed | specs/claude-events-config/spec.md | CLI `can_use_tool` control request 关联当前 Session/turn/tool，并调用 ACP `session/requestPermission`；发给客户端的选项只包含固定 v0.70.0 语义下当前请求有效的 allow/reject 选择。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A31 | passed | specs/claude-events-config/spec.md | ACP 选择严格校验后转换为 control response；更新输入、permission updates 或 message 的可选字段只在 upstream 允许时返回。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A32 | passed | specs/claude-events-config/spec.md | request context 取消、Session/turn stale、客户端取消、未知 option、connection 未注入、发送失败、close 或 process exit 全部 fail closed，并确保 CLI 的 control request 最终得到拒绝/取消或随进程关闭解除。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A33 | passed | specs/claude-events-config/spec.md | permission mode 不得在错误/未知配置时扩大权限；危险 bypass 受 upstream/root 约束，默认模式保持可提示的安全边界。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A34 | passed | specs/claude-events-config/spec.md | Session 创建后从 control initialize/固定基线信息建立 model、effort、fast 与 permission mode 选项；只宣告运行时实际返回且 Go 能设置的值。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A35 | passed | specs/claude-events-config/spec.md | model 变化使用 `set_model`，effort/fast 使用固定基线对应的 `apply_flag_settings` 或明确 control request，thinking budget 使用 `set_max_thinking_tokens`，permission mode 使用 `set_permission_mode`。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A36 | passed | specs/claude-events-config/spec.md | config/mode 更新在 Session 内串行；非法 ID/value、CLI 拒绝、query 已关闭或 Session 不存在时返回可诊断错误，不静默成功。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A37 | passed | specs/claude-events-config/spec.md | load/resume 恢复后的实际配置是响应真值；客户端覆盖只在 Session 成功建立后应用。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A38 | passed | specs/claude-events-config/spec.md | additional directories 规范化并去重后映射为独立 `--add-dir` 参数；相对路径按 Session cwd 解析，非法/不可访问目录返回明确错误。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A39 | passed | specs/claude-events-config/spec.md | stdio MCP 保留 name、command、args 和 env；HTTP/SSE MCP 保留 name、URL 与 headers，并编码为 Claude CLI 接受的 `--mcp-config` JSON。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A40 | passed | specs/claude-events-config/spec.md | MCP 名称冲突、协议类型错误、必填字段缺失或不可编码值在启动前失败；secret header/env 不进入日志、错误详情或 fixture。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A41 | passed | specs/claude-events-config/spec.md | A2：initialize capability 准确性。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A42 | passed | specs/claude-events-config/spec.md | A10-A11：输入、消息、usage 与 plan。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A43 | passed | specs/claude-events-config/spec.md | A12-A13：tool call 与 permission。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A44 | passed | specs/claude-events-config/spec.md | A14-A15：配置、additional directories 和 client MCP。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A45 | passed | specs/claude-events-config/spec.md | A18：mapper/fake CLI/回归验证与中文注释。 | 已逐项审阅事件、内容、工具、权限、配置、目录与 MCP 实现及其自动化证据，满足该规格条目。 |
| A46 | passed | specs/claude-protocol-upstream/spec.md | 行为基线固定为 `agentclientprotocol/claude-agent-acp` tag `v0.70.0`、commit `d0aafb1ca26427285ffaeac8d8a4452fff28e9c3`。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A47 | passed | specs/claude-protocol-upstream/spec.md | 协议实现基线固定为该 tag 使用的 `@anthropic-ai/claude-agent-sdk` `0.3.232`；ACP 行为参考其 `@agentclientprotocol/sdk` `1.3.0`，本仓库协议边界继续使用 acp-go-sdk `v0.13.5`。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A48 | passed | specs/claude-protocol-upstream/spec.md | `.upstream/claude-agent-acp` 是被 Git 忽略的本地 clone；实现时按固定 tag 读取 `src/acp-agent.ts`、`src/tools.ts`、`src/settings.ts`、相关 tests/fixtures 及 Agent SDK public declarations，不以 moving main 或 README 摘要替代源码证据。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A49 | passed | specs/claude-protocol-upstream/spec.md | `agents/claude/protocol` 定义 Claude CLI stream-json 所需的窄类型 envelope，至少区分 system、assistant、user、result、stream_event、tool_progress、control_request、control_response、control_cancel_request 和 keep_alive。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A50 | passed | specs/claude-protocol-upstream/spec.md | control subtype 至少覆盖 initialize、can_use_tool、interrupt、set_permission_mode、set_model、set_max_thinking_tokens 和 apply_flag_settings；未纳入 subtype 保留原始 JSON 并走兼容路径。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A51 | passed | specs/claude-protocol-upstream/spec.md | envelope 先解析稳定 discriminator，再解码已知 payload；optional/nullable、未知字段和 content union 的语义保持 upstream，不以易碎的单一大结构体吞掉所有消息。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A52 | passed | specs/claude-protocol-upstream/spec.md | CLI stdout 按逐行 JSON 解码；非 JSON/未知消息按固定基线记录有界安全摘要并继续，超限帧或不可恢复的 pipe/process 错误结束对应 Session。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A53 | passed | specs/claude-protocol-upstream/spec.md | 发往 CLI 的 JSONL 由单写者或等价锁串行化；control request ID 在 Session 内唯一，响应/取消严格关联 pending request，close/exit 会解除全部 pending。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A54 | passed | specs/claude-protocol-upstream/spec.md | 仓库 `UPSTREAM.md` 记录固定版本、纳入/跳过能力、Claude ACP/Agent SDK symbol 到 Go package/function/test 的映射、Go 等价改写、fixture 来源和升级步骤。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A55 | passed | specs/claude-protocol-upstream/spec.md | 代码注释只解释当前 Go 代码的职责、状态、顺序、失败语义和复杂逻辑；版本来源、对照 symbol、fixture 与移植差异不得写进代码，统一由 `UPSTREAM.md` 承担。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A56 | passed | specs/claude-protocol-upstream/spec.md | 不提交 Agent SDK minified bundle、npm tarball 或 upstream clone；只提交为测试裁剪且注明来源的最小 fixture，避免包含凭据、真实路径和用户 transcript。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A57 | passed | specs/claude-protocol-upstream/spec.md | upstream 升级必须作为显式变更：先更新固定版本和 fixture，再审计协议/行为 diff，最后修改 Go runtime 与验收证据。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A58 | passed | specs/claude-protocol-upstream/spec.md | A4-A5：JSONL/control 协议与异常兼容。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A59 | passed | specs/claude-protocol-upstream/spec.md | A17：固定 upstream 与可审计映射。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A60 | passed | specs/claude-protocol-upstream/spec.md | A18：fixture 和完整自动化证据。 | 已逐项核对窄协议、control、冻结 fixture 与 UPSTREAM.md 集中追踪，满足该规格条目。 |
| A61 | passed | specs/claude-runtime/spec.md | Claude CLI 由用户预装。`CLAUDE_CODE_EXECUTABLE` 非空时只使用该显式路径；为空时从 PATH 查找 `claude`。显式路径无效或 PATH 无可执行文件时返回清晰错误，不下载、不捆绑、不启动 Node。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A62 | passed | specs/claude-runtime/spec.md | 构造时以有界命令获取版本并写 stderr 诊断；没有已确认的 CLI semver 兼容范围时不伪造硬性版本判定，协议兼容基线由 Agent SDK 0.3.232 fixtures 表达。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A63 | passed | specs/claude-runtime/spec.md | 每个 ACP Session 启动独立 Claude CLI，基础参数是 `--output-format stream-json --verbose --input-format stream-json`；权限回调使用 `--permission-prompt-tool stdio`，其余参数由 Session 配置、MCP 和 additional directories 决定。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A64 | passed | specs/claude-runtime/spec.md | stdout 只进入 Claude JSONL reader，stderr 进入有界/脱敏 logger，stdin 写入由 transport 串行化；非零退出、信号退出、写失败和 EOF 保留稳定错误类别与有限 stderr tail。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A65 | passed | specs/claude-runtime/spec.md | Session close 先解除 prompt/control/permission 等待，再结束 stdin、温和终止进程并在有界期限后强制终止；重复 close 与 Adapter Close 幂等。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A66 | passed | specs/claude-runtime/spec.md | new session 使用 cwd、additional directories、client MCP 与初始配置构造进程，完成 control initialize/首个 init 消息握手后才把 Session 发布到 store。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A67 | passed | specs/claude-runtime/spec.md | ACP SessionID 使用 Claude 返回的持久 Session ID；构造失败、关闭竞态或迟到 init 不得安装残缺 Session。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A68 | passed | specs/claude-runtime/spec.md | resume 使用 `--resume=<session-id>` 恢复并返回配置能力，不向客户端回放既有消息。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A69 | passed | specs/claude-runtime/spec.md | load 使用相同恢复身份并按 v0.70.0 语义回放可表达历史；过滤 local-command marker、system/internal-only 消息和 V1 不支持的扩展，保持 user/assistant/tool 顺序。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A70 | passed | specs/claude-runtime/spec.md | 同一 SessionID 在 cwd/MCP/additional directories 等定义参数改变时关闭旧进程并按新参数重建，不能复用错误进程；失败时不污染其他 Session。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A71 | passed | specs/claude-runtime/spec.md | close session 仅作用于活动内存 Session，不删除磁盘 transcript；不存在或已经关闭的 Session 返回稳定错误/幂等语义按 ACP 方法契约处理。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A72 | passed | specs/claude-runtime/spec.md | 每个 Session 维护一个长期 consumer 和 FIFO turn queue；prompt 创建唯一 user-message UUID，先安装 turn waiter 再写入输入，避免 echo/result 早到竞态。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A73 | passed | specs/claude-runtime/spec.md | consumer 按 user echo 激活对应 turn，按 stream/assistant/result/idle 结束；每个 prompt 恰好 settle 一次，多轮顺序和 usage 不串线。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A74 | passed | specs/claude-runtime/spec.md | stream 在无 result、异常结束或进程死亡时解除所有 turn；Session 进入不可恢复状态后，后续 prompt 返回“新建 Session”类型的清晰错误而不写入死 pipe。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A75 | passed | specs/claude-runtime/spec.md | queue、partial content、tool cache 和迟到/orphan 记账全部有界；旧 UUID/result/idle 不得激活或完成后续 turn。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A76 | passed | specs/claude-runtime/spec.md | cancel 标记当前 generation，发送 interrupt control request，立即取消尚未开始的排队 turn，并在有界 grace 后解除卡住的活动 prompt；重复 cancel 幂等。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A77 | passed | specs/claude-runtime/spec.md | cancel 后的 echo/result/idle 只用于清理对应 orphan，不得完成新 turn；cancel 与 close/process exit 的先后顺序不造成永久等待。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A78 | passed | specs/claude-runtime/spec.md | `_session/steering` 复用 acp-go-sdk extension handler。活动 turn 中将带唯一 UUID 和 `priority: "now"` 的用户消息注入同一输入流，并让原 prompt 在正确 idle 后完成。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A79 | passed | specs/claude-runtime/spec.md | Session 空闲时按 v0.70.0 默认行为启动 detached turn 并返回 `startedNewTurn`；支持 upstream `promptRequired` idle behavior 时只返回提示，不偷写输入。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A80 | passed | specs/claude-runtime/spec.md | steering、prompt、cancel、close 在一个 Session 的状态锁/fence 下判定，不能产生 rival turn；一个 steering 失败不破坏 Session 后续请求。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A81 | passed | specs/claude-runtime/spec.md | A3：CLI 解析与版本诊断。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A82 | passed | specs/claude-runtime/spec.md | A4-A5：进程/transport 生命周期和错误。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A83 | passed | specs/claude-runtime/spec.md | A6-A7：new/load/resume Session。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A84 | passed | specs/claude-runtime/spec.md | A8-A9：prompt、steering、cancel。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A85 | passed | specs/claude-runtime/spec.md | A16：关闭/退出竞态和资源回收。 | 已逐项审阅 CLI、Session、历史、FIFO、cancel、steering 和关闭状态机及其测试，满足该规格条目。 |
| A86 | passed | specs/framework/spec.md | `acp-agent` 直接使用 acp-go-sdk v0.13.5 的 `AgentSideConnection`、Agent 类型、可选 Loader 和 ExtensionMethodHandler，不实现自己的 ACP JSON-RPC connection、dispatch 或 cancel 层。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A87 | passed | specs/framework/spec.md | 协议服务 stdout 只写 ACP 帧，启动、版本和运行时诊断只写 stderr。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A88 | passed | specs/framework/spec.md | Core 只定义 Adapter 元数据、工厂、注册/选择和通用生命周期边界，不包含 Codex 或 Claude 的 session/turn/process/tool/permission 类型。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A89 | passed | specs/framework/spec.md | 默认选择名保持 `codex`；无参数与显式 `--adapter codex` 等价，显式 `--adapter claude` 构造 Claude，未知名称在建立 ACP connection 前 fail fast。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A90 | passed | specs/framework/spec.md | composition root 同时显式注册 Codex 与 Claude 工厂；未选中的工厂不得执行或探测对应 CLI。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A91 | passed | specs/framework/spec.md | composition root 将 stderr logger 和 `CLAUDE_CODE_EXECUTABLE` 值显式传给 Claude；Claude 包自行完成 PATH fallback。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A92 | passed | specs/framework/spec.md | 可选能力继续使用消费方小接口；构造函数返回具体类型，禁止全局注册、service locator、DI 容器和覆盖所有 Adapter 的 Runtime 大接口。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A93 | passed | specs/framework/spec.md | Claude 可复用 acpserver 已有 connection binder 与 adapter closer 注入/清理时序；除非实现发现经测试证明的 SDK 能力缺口，不修改 server 协议职责。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A94 | passed | specs/framework/spec.md | ACP 连接结束或进程 context 取消时，acpserver 在独立有界 context 中调用 Claude Close；Close 释放所有 Session 进程和等待者并保持幂等。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A95 | passed | specs/framework/spec.md | A1：默认/显式 Adapter 选择及 Codex 无回归。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A96 | passed | specs/framework/spec.md | A2：Claude capability 只声明已实现功能。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A97 | passed | specs/framework/spec.md | A16：connection/Adapter 关闭释放所有资源。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |
| A98 | passed | specs/framework/spec.md | A18：组合根、回归和工程验证。 | 已逐项审阅 acp-go-sdk 复用、组合根选择、stdout 隔离和连接关闭边界，满足该规格条目。 |

## Checks

| Check | Command | Working directory | Status | Exit | Duration |
| --- | --- | --- | --- | ---: | ---: |
| 全量 Go 测试 | test ./... -count=1 -timeout=300s | . | passed | 0 | 7555 ms |
| 全量竞态测试 | test -race ./... -count=1 -timeout=600s | . | passed | 0 | 9230 ms |
| Go 静态检查 | vet ./... | . | passed | 0 | 127 ms |
| 协议生成一致性检查 | run ./tools/protocolgen --check | . | passed | 0 | 758 ms |
| 中文声明注释审计 | test ./internal/codex -run TestHandwrittenGoDeclarationsHaveChineseComments -count=1 | . | passed | 0 | 318 ms |
| Git diff 空白检查 | diff --check | . | passed | 0 | 17 ms |

## Blockers

_None._

## Risks and skipped work

- 未执行需要真实账号、网络和潜在费用的 Claude CLI opt-in smoke；发布证据限定为 fake CLI、冻结 fixture 与构建验证。
- 后续 Claude CLI 若改变 stream-json/control 行为，需要按 UPSTREAM.md 的显式升级流程重新冻结证据并审计差异。

## Previous iterations

| Goal cycle | Iteration | Attempt | Outcome | Unresolved | Summary | Completed |
| ---: | ---: | ---: | --- | --- | --- | --- |
| 1 | 1 | 0 | recovery | — | Native confirmed acceptance criteria changed | 2026-08-24T03:50:24.856Z |
| 2 | 1 | 1 | recovery | — | Verifier 复核发现取消超时、并发队列、进程回收、历史路径、unknown tool 与权限建议边界需要修订；保持确认规格不变，修复后提交新候选。 | 2026-08-24T04:01:15.546Z |
| 2 | 2 | 1 | pass | — | 修复候选满足 A1-A98：Claude V1 功能、并发生命周期、安全边界、集中 upstream 追踪、中文注释和工程验证均通过；未发现阻断项。 | 2026-08-24T04:03:31.909Z |

## Conclusion

修复候选满足 A1-A98：Claude V1 功能、并发生命周期、安全边界、集中 upstream 追踪、中文注释和工程验证均通过；未发现阻断项。
