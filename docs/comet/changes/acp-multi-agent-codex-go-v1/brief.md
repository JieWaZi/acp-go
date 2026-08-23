# Outcome

交付一个以 `github.com/coder/acp-go-sdk` 为 ACP 协议基础、以固定版本的 `agentclientprotocol/codex-acp` 为行为基准的 Go ACP Agent。V1 采用轻量 Ports-and-Adapters，提供具备必要扩展点但不过度抽象的 Agent-neutral Core，并完成首个 Codex Adapter，使 ACP Client 能通过 stdio 驱动 Codex app-server 的会话、流式输出、工具事件、审批、控制和基础配置能力；代码结构应方便后续按 codex-acp 增量同步修复。

当前仓库没有任何实现或 Git commit；本变更是从零建设新系统，而不是对已有 Go 流程的小范围改造。

# Scope

## 系统边界

- ACP Client 与本进程之间使用 ACP/JSON-RPC，协议连接、请求分发、取消和扩展机制复用 acp-go-sdk。
- ACP Core 只负责连接、Adapter 选择/生命周期、能力发现以及通用上下文和日志入口；不得持有 Codex Thread、Turn、Sandbox、Approval 等专属状态。
- Codex Adapter 启动 `codex app-server`，通过 stdin/stdout JSON-RPC 通信，并在 Adapter 内维护 Codex 会话状态、事件路由、审批、steering、认证与配置。
- V1 不分发 Codex：用户负责安装 Codex；若设置 `CODEX_PATH` 则使用该路径，否则从 PATH 查找。0.148.0 是验证基线，其他可执行版本在 stderr 输出兼容性警告后继续尝试。
- Adapter Contract 使用小型核心能力与按需可选能力，不预先制造涵盖所有未来 Agent 的巨大 Runtime 接口。
- 工程采用轻量 Ports-and-Adapters：Core contracts 是端口，Codex 是首个适配器，`cmd/acp-agent` 是显式 composition root；不增加独立 use-case、repository、domain entity 或 DDD 层。
- V1 交付单一 `acp-agent` 可执行程序；不传 `--adapter` 时默认选择 Codex，也接受显式 `--adapter codex`。未来新增 Adapter 不改变默认值，并通过参数显式选择。
- 本变更只完成本地开发、验证和本地 Git 提交；`go.mod` 暂用本地模块名 `acp-go`。远端 module path、push、PR 和发布流程留给后续独立变更。
- ACP 协议层直接使用 acp-go-sdk 已有公共 API，不另写重复的 connection、dispatch、cancel 或 extension 协议实现。
- Codex 已有可参考的业务路径、竞态处理、异常兜底和测试时，Go V1 直接做等价移植；只有语言/运行时机制差异允许局部重写。
- 开发前必须把固定快照的 `agentclientprotocol/codex-acp` 源码克隆到本地被 Git 忽略的 `.upstream/codex-acp`；实现时直接对照其源文件、symbol 和 fixture，不能只依赖 README、二手摘要或自行猜测行为。

## V1 功能

- ACP initialize 与 Codex Adapter 能力声明。
- Codex app-server 进程启动、初始化、stdio 通信、退出和资源释放。
- 新建、加载/恢复 Session；多轮 Prompt/Turn、流式输出和完成路由。
- Cancel/Interrupt 与每 Session 串行、失败隔离的 Steering。
- Agent Message、Reasoning、基础 Plan、Token Usage 事件映射。
- Command、File Change、MCP Tool Call 到 ACP ToolCall 的基础映射。
- Command、File Change、Permissions 三类审批及 fail-closed 行为。
- Model、Reasoning Effort、Sandbox/Agent Mode 基础配置。
- Text、Image、Resource 输入和 ChatGPT/API Key 基础认证。
- early completion、stale turn/stale approval 和未知事件的安全处理。
- 可追踪的上游版本、模块映射、语义裁剪与同步记录。

## Source coverage

直接需求源：`/Users/ryan/Downloads/acp-multi-agent-codex-go-v1-plan.md`，共 428 行、19,658 字节；文件正文已完整读取。下表按标题、段落/列表、表格、代码块、约束和链接划分来源单元。

| ID | 来源位置 | 读取状态 | 保留语义 | Spec / 验收映射 | 覆盖状态与理由 |
| --- | --- | --- | --- | --- | --- |
| S01 | L1-L10 标题、版本、状态、项目原则 | complete | V1 是裁剪范围后的 Go 行为移植，并为多 Agent 留出结构边界 | `framework` / A1-A2；`protocol-upstream` / A16 | covered：目标背景及总原则已保留 |
| S02 | L11-L21 项目目标与核心定位列表 | complete | 复用 ACP Go SDK、以 codex-acp 为行为基准、裁剪能力而非已纳入语义、Core 保持中立 | `framework` / A1-A2；`protocol-upstream` / A16 | covered |
| S03 | L22-L30 非目标列表与关键结论 | complete | 不逐行翻译、不主动改写稳定语义、不提前抽象所有 Agent、不做全量 Codex 扩展 | `# Non-goals` | non-goal：明确排除项，不产生独立验收 ID |
| S04 | L31-L41 Upstream First 段落与列表 | complete | 保留上游状态、事件、审批、队列、stale/early-completion 和异常兜底语义；Go 差异需记录 | `protocol-upstream` / A13、A16 | covered：基线已固定到 2026-08-23 复核的最新稳定快照 |
| S05 | L42-L54 “裁剪能力，不裁剪语义”正文与示例代码块 | complete | 整体排除未纳入能力；已纳入能力不得再做语义弱化 | `protocol-upstream` / A16；`codex-events` / A10 | covered |
| S06 | L55-L58 Core/Adapter 特化边界 | complete | 仅通用生命周期、注册、能力和连接进入 Core | `framework` / A2 | covered |
| S07 | L59-L88 总体架构正文与图 | complete | ACP Client → Core → 多 Adapter；Codex Adapter → app-server | `framework` / A2；`codex-runtime` / A3 | covered |
| S08 | L89-L96 ACP Core 职责列表 | complete | 连接、注册选择、能力、Session 标识/context/log；禁止 Codex 状态泄漏 | `framework` / A1-A2 | covered：单一 `acp-agent` 默认 Codex并支持显式 `--adapter codex`，未知 Adapter fail fast |
| S09 | L97-L103 Agent Adapter 职责列表 | complete | Agent 原生状态、请求转换、事件映射及专属恢复/审批/steering/配置/认证 | `framework` / A2；`codex-runtime` / A3-A7；`codex-events` / A8-A10；`codex-config` / A11-A12 | covered |
| S10 | L104-L144 推荐工程结构代码块与说明 | complete | Go 包按 Core、Adapter、Codex 特化与协议类型组织；Codex 模块与上游主要模块可对应 | `framework` / A2；`protocol-upstream` / A16 | covered：采用轻量 Ports-and-Adapters；目录名允许按 Go package 调整，但不增加 Clean/DDD 层且保持上游职责可追踪 |
| S11 | L145-L161 Adapter 抽象正文、结构代码块与理由 | complete | 稳定核心接口加 Optional Capability Interfaces | `framework` / A2 | covered：接口在消费方定义、保持小型；避免为单实现预建无用层次 |
| S12 | L162-L184 V1 支持能力表 | complete | initialize、进程、session、turn、stream、控制、事件、工具、审批、配置、输入、认证、未知事件 | 所有 capability Specs / A1、A3-A14 | covered |
| S13 | L185-L196 V1 不支持能力表与解释 | complete | Goal、Review、Gateway、全部 Slash、AIR、Typed Failure、File Report、特殊事件全集、完整 E2E/跨平台专项排除 | `# Non-goals` | non-goal：整体排除而非周边逻辑简化 |
| S14 | L197-L212 上游 TypeScript→Go 模块映射表 | complete | 关键 Go 模块可追溯到对应上游模块 | `protocol-upstream` / A16 | covered：映射基于 codex-acp 1.6.2/`ba5bcc3…`，后续修复通过独立增量变更同步 |
| S15 | L213-L223 UPSTREAM.md 列表 | complete | 记录 codex-acp、Codex/schema 版本、跳过模块、文件映射、等价改写与同步历史 | `protocol-upstream` / A16 | covered：版本锚点与升级策略已确认 |
| S16 | L224-L240 app-server 生命周期正文与图 | complete | spawn `codex app-server`、stdio JSON-RPC、进程退出后 dispose/cancel，不引入 HTTP/Unix Socket | `codex-runtime` / A3、A13 | covered |
| S17 | L241-L252 Steering 正文、队列图与约束 | complete | 每 Session FIFO 串行；并发请求不产生 rival turn；单次失败不阻塞后续 | `codex-runtime` / A7 | covered |
| S18 | L253-L275 Approval 正文、流程图与异常列表 | complete | 三类请求映射到 ACP permission；决定回传；异常/缺失 handler fail closed | `codex-events` / A10 | covered |
| S19 | L276-L279 Turn Completion / Stale Turn | complete | 保留 early completion、stale notification、旧 turn 审批竞态保护 | `codex-runtime` / A13；`codex-events` / A10 | covered：按固定 codex-acp 1.6.2 基线移植并通过对应测试审计 |
| S20 | L280-L302 Session 状态正文、图与结论 | complete | 通用 ACP Session 关系在 Core；Codex Thread/Turn/config/usage/MCP/queue/approval 状态留在 Adapter | `framework` / A2；`codex-runtime` / A4-A7；`codex-config` / A11 | covered |
| S21 | L303-L321 Protocol 生成正文、图与规则列表 | complete | 从 Codex app-server schema 生成 Go 类型；generated 文件不手改；行为依赖生成类型；升级先再生类型 | `protocol-upstream` / A15 | covered：生成器、生成快照和 freshness 检查均为 V1 发布硬门槛 |
| S22 | L322-L332 六阶段执行计划表 | complete | 从骨架/协议到 session、events、approval、steering/config 和稳定性逐阶段交付 | `verification` / A17，并关联 A1-A16 | covered：作为实施排序输入，不把人天估算视为行为验收 |
| S23 | L333-L348 十三项 V1 验收列表 | complete | initialize、session、turn、事件、审批、控制、配置、usage、退出、stale、unknown、追溯均可观察验证 | `# Acceptance examples` 与全部 Specs / A1-A17 | covered：合并重复语义后形成非重复验收项 |
| S24 | L349-L363 工作量表与 9-12 人天结论 | complete | 原计划基线约 10 人天 | 无 | background：估算不是产品语义；将在范围/测试/生成器决策后重新评估 |
| S25 | L364-L389 多 Agent 演进正文与路线图 | complete | V1 只做 Codex；后续 Adapter 用来反向验证并稳定 Core | `framework` / A2；`# Non-goals` | covered/background：V1 保留边界，第二个真实 Adapter 不在本变更 |
| S26 | L390-L397 长期规则列表 | complete | ACP 复用 SDK、Agent 优先移植、Core 只上移验证共性、能力可不同、版本可追踪 | `framework` / A1-A2；`protocol-upstream` / A16 | covered |
| S27 | L398-L408 风险控制表 | complete | 控制过度抽象、上游偏离、schema 漂移、竞态、未知事件和 Codex 状态泄漏 | `framework` / A2；`codex-runtime` / A13；`protocol-upstream` / A15-A16；`verification` / A17 | covered |
| S28 | L409-L420 立项结论列表 | complete | 汇总 V1 范围、行为基准、SDK、steering、approval、排除项、多 Agent 边界、生成器和估算 | 所有 Specs / A1-A17；`# Non-goals` | covered：上游版本与生成器范围歧义均已确认 |
| S29 | L421-L425 coder/acp-go-sdk 链接 | complete | ACP Go SDK 是协议基础 | `framework` / A1-A2 | covered：已审计 v0.13.5/`0845a3b…` 的 `Agent`、可选 `AgentLoader`、`Client`、`AgentSideConnection`、取消、extension、schema 与 JSON parity/notification barrier 测试；V1 直接组合这些公共类型和连接，不复制协议层 |
| S30 | L426 codex-acp 链接 | complete | Codex 行为与模块参考实现 | `protocol-upstream` / A13、A16；Codex Specs / A3-A14 | covered：已审计 1.6.2/`ba5bcc3…` 的 Server、AppServerClient、JSON-RPC、EventHandler、ToolCallMapper、ApprovalHandler、SteeringQueue 及 V1 对应 initialize/session/event/tool/approval/steer/config/process-exit fixtures；纳入行为均有直接移植入口，排除模块已进入 Non-goals |
| S31 | L427 OpenAI Codex 链接 | complete | Codex CLI/App Server schema 上游 | `codex-runtime` / A3；`protocol-upstream` / A15 | covered：已审计 `codex app-server` 的稳定 schema 生成命令与固定 0.148.0 快照中 V1 所需 thread/turn/event/approval 类型；生成器默认排除 experimental，V1 使用固定 JSON Schema 输入并做 freshness 校验 |
| S32 | L428 ACP 协议站点链接 | complete | ACP 规范是外部协议真值 | `framework` / A1-A2；`verification` / A17 | covered：已通过 v0.13.5 随附的规范 schema 与文档引用审计 initialize、session new/load/resume/cancel/prompt、config、message/reasoning/plan/usage、tool call 与 permission 语义；V1 能力声明只宣告实际实现且按 SDK 校验 |

# Non-goals

- 不做 TypeScript 源码逐行 1:1 翻译。
- 不主动重新设计已纳入范围的 codex-acp 状态机、异常策略、并发顺序或 fail-safe/fail-closed 行为。
- V1 不实现 Goal、Review、Gateway Auth、AIR Extensions、Typed Session Failure、Agent File Report、全部 Slash Commands、全部特殊事件或完整 E2E 套件；macOS 与 Linux 正式支持，Windows 不做专项运行/集成保证。
- V1 不实现第二个真实 Agent Adapter，也不为了假想的未来 Agent 上移 Codex 专属模型。
- V1 的 MCP 范围是已有 Codex MCP tool event 的 ACP ToolCall 映射，不包含 client-provided MCP server 配置、传输或生命周期扩展。
- V1 的输入范围不包含 ACP Audio；附件仅纳入 Text、Image、Resource。
- 不新增 HTTP、WebSocket 或 Unix Socket 作为 Codex app-server 传输。
- 不把 Codex 二进制捆绑进 `acp-agent` 发布制品，也不在运行时下载、升级或管理 Codex。

# Acceptance examples

- A1：运行 `acp-agent` 或 `acp-agent --adapter codex` 均启动 Codex Adapter，ACP Client 通过 stdio 完成 initialize 并收到与 V1 实际能力一致的 Agent capability/auth/config 声明；未知 Adapter 在写出协议内容前返回可诊断启动错误；协议连接、分发、取消与扩展基础由固定版本 acp-go-sdk 提供。
- A2：Core 能注册并选择 Codex Adapter，且 Core 的公开状态/API 不出现 Codex Thread、Turn、Sandbox、Approval 类型；可选能力通过小接口按需发现。
- A3：`acp-agent` 按 `CODEX_PATH` → PATH 解析 Codex；找不到或显式路径无效时以可诊断错误退出；非 0.148.0 版本在 stderr 警告后继续。启动 Agent 后只生成一个受管的 `codex app-server` stdio JSON-RPC 连接；初始化成功后可请求；进程退出或关闭时连接和活动请求被释放/取消。
- A4：ACP new session 创建 Codex thread；load/resume 恢复已有 thread 和必要历史/配置；不存在、失效或并发关闭的 session 返回稳定错误而不污染其他 session。
- A5：同一 session 可连续完成多轮 prompt；Agent Message 与 Reasoning 流式到达，turn completion 恰好结束对应 prompt。
- A6：prompt 启动前或活动 turn 中收到 cancel/interrupt 时，不再启动无效 turn，或调用 Codex turn interrupt；最终返回 ACP cancelled 语义且不影响后续 turn。
- A7：同一 session 的并发 steering 严格按到达顺序执行；活动 turn 使用 steer，无活动 turn 时按上游规则启动新 turn；任何单个请求失败后队列继续，竞态不会产生 rival turn。
- A8：Agent Message、Reasoning、基础 Plan 和 Token Usage 核心事件稳定转换为对应 ACP session update，增量/完成事件不产生重复最终内容。
- A9：Command、File Change 与 MCP Tool Call 的开始、增量/更新和完成状态映射为关联一致的 ACP ToolCall；必要位置、diff、原始输入和输出在 V1 可表达范围内保留。
- A10：Command、File Change、Permissions 三类 Codex 审批映射为 ACP `session/requestPermission` 并把有效选择回传；取消、异常、handler 缺失或 stale approval 均 fail closed，且不会作用于新 turn。
- A11：新建/恢复 session 可按 V1 暴露的选项设置 model、reasoning effort 与 sandbox/agent mode；值校验和模式映射与固定上游基线一致。
- A12：Text、Image、Resource 输入能转换到 Codex turn input；ChatGPT 与 API Key 两种基础认证能被声明、触发并返回成功或可诊断失败。
- A13：turn completion 先于等待器注册、旧 turn 事件晚到、旧审批晚到或 app-server 异常退出时，当前活动 session/turn 不被错误完成、批准或卡死。
- A14：遇到固定基线之外的 Codex event 时记录可诊断日志并安全忽略，不导致 ACP session 或进程崩溃。
- A15：固定 Codex 版本的稳定 app-server schema 可通过受控命令重复生成 Go 类型；生成文件带来源且禁止手改，手写运行时代码只依赖生成类型。
- A16：`UPSTREAM.md` 固定 SDK/codex-acp/Codex schema 版本，列出纳入与跳过能力、TS→Go 映射、Go 等价改写和同步历史；关键并发/异常代码可追溯到上游行为或测试。
- A17：自动化验证至少覆盖 macOS 与 Linux 的构建、单元、竞态/取消/队列、JSON-RPC 进程模拟、核心事件与审批 fixture、以及可控的关键路径 app-server 集成；Windows 尽力保持可编译但不阻断发布；每项 V1 验收都有可重复证据。
- A18：所有手写 Go 类型、结构体字段和函数具有清晰中文注释；函数内部的关键状态转换、并发、异常兜底和复杂映射说明“为什么”以及对应上游逻辑，简单自明语句不堆叠逐行注释。

# Constraints and invariants

- 行为优先级：确认后的 V1 Spec > 固定的 codex-acp 行为基线 > Go 工程偏好；Go 改写不得改变可观察业务语义。
- 使用 `context.Context` 传播取消和生命周期；goroutine/channel 必须有明确所有者、终止条件和有界资源策略。
- command/file/permissions 审批异常始终 fail closed。
- per-session steering 队列 FIFO、单消费者、单请求失败隔离；session 结束必须清理队列与等待者。
- completion、notification 和 approval 必须按 thread/turn/session generation 或等价身份检查防止 stale 事件串线。
- unknown event 可兼容忽略，但核心 V1 event 不得因走 default 分支而静默丢失。
- Go 接口在消费方定义，优先 1-3 方法的小接口；构造函数返回具体类型；仅因第二实现或测试边界而抽取接口。
- V1 使用显式构造和手工依赖注入；不使用全局 service locator、隐式 `init()` 注册或 DI 容器库。
- 遵循 `golang-design-patterns`：仅在配置确实需要演进时使用可校验的 functional options；错误优先返回并保持主路径扁平；预期失败不 panic；资源获取后及时安排关闭；外部请求有 context、超时和有界资源。
- “必要扩展性”只覆盖已知变化点：Adapter 注册/选择、可选能力、Codex 进程/协议边界和 mapper；不为尚不存在的 Agent 状态、统一 Runtime 或未知扩展预建层次。
- ACP 功能优先直接组合 acp-go-sdk 类型和连接，不包装出语义重复的内部协议框架。
- Codex 功能优先保持与固定 codex-acp 文件、symbol 和 fixture 的职责对应，便于通过差异审计增量同步修复。
- `.upstream/codex-acp` 必须是可检查的本地 Git clone，HEAD 固定到 `ba5bcc3d7759250dde9d4d2286a1bec11b363208`；每个 Codex 关键模块在实现和测试前读取对应上游源代码，不得用自创状态机替代可直接移植的实现。
- 所有手写 Go 类型、结构体字段和函数使用清晰中文注释；关键/复杂内部逻辑注释状态、顺序、失败语义和上游来源。Protocol Generator 输出豁免中文要求，只保留标准生成标记和上游 schema 原始文档，禁止手工修改。
- 外部进程和请求必须可取消、有超时/退出处理；队列、缓冲和并发活动不得无界增长。
- 当前 Codex app-server 只以稳定 API surface 为默认候选；采用实验字段/方法必须成为明确、可验证的 V1 决策。

# Decisions

- 2026-08-23：使用 Comet Native；change 名为 `acp-multi-agent-codex-go-v1`。
- 2026-08-23：用户选择在当前目录和当前 `main` 分支进行 Shape，不创建额外 branch/worktree。
- 2026-08-23：附件是完整需求源而非执行指令；所有可执行语义必须进入 Spec 和至少一个验收 ID。
- 2026-08-23：问题分类为 architectural；在 Shape 明确确认前不实现代码。
- 2026-08-23：仓库为空项目；不存在需要兼容的本地实现或既有 Go 包结构。
- 2026-08-23：Adapter/Core 采用显式构造与手工依赖注入、小型消费方接口；不引入 DI 框架，除非后续规模事实证明有必要并重新确认。
- 2026-08-23：编码采用 `golang-design-patterns`，但设计模式只解决真实生命周期、配置演进和测试边界，不以模式本身增加层次。
- 2026-08-23：扩展性限定为必要扩展点，不提前抽象未知 Agent 共性。
- 2026-08-23：ACP 基础必须直接复用 acp-go-sdk；Codex V1 对可参考的 codex-acp 实现和测试做等价移植，不重复实现已有协议或业务语义。
- 2026-08-23：文件/职责/关键注释与 upstream 映射需支持后续 codex-acp 增量修复审计。
- 2026-08-23：手写 Go 类型、字段、函数以及关键/复杂函数内部逻辑使用中文注释。
- 2026-08-23：上游采用本日复核的最新稳定快照并精确固定：acp-go-sdk v0.13.5/`0845a3bb9eddda5bfc22a94dd3598c90cb842451`、codex-acp 1.6.2/`ba5bcc3d7759250dde9d4d2286a1bec11b363208`、其 lockfile 中 `@agentclientprotocol/sdk` 1.4.0 与 `@openai/codex` 0.148.0。
- 2026-08-23：开发期间不追踪 moving main；后续上游修复或版本升级使用独立、可审计的增量变更同步。
- 2026-08-23：Codex 0.148.0 的默认稳定 app-server schema 是 V1 类型输入；实验 API 未被附件纳入，因此不生成、不宣告也不调用实验 surface。
- 2026-08-23：Protocol Generator 是 V1 发布硬门槛；V1 必须包含稳定 JSON Schema→Go 的可重复生成、提交的生成快照、禁止手改标记和 freshness 检查，不能延期到 V1.x。
- 2026-08-23：Go 工程采用轻量 Ports-and-Adapters：只保留 Core contracts、Codex Adapter、protocol package 和显式 composition root；不采用纯平铺结构，也不引入完整 Clean Architecture/DDD 分层。
- 2026-08-23：V1 只交付单一 `acp-agent` composition root；默认 Adapter 永久保持 Codex，同时支持 `--adapter codex`。未来 Adapter 必须显式选择，不拆分独立二进制。
- 2026-08-23：Codex 由用户预装；`CODEX_PATH` 优先于 PATH，显式路径无效时不静默回退。0.148.0 是验证基线，其他版本在 stderr 输出兼容性警告后继续尝试；V1 不捆绑或自动下载 Codex。
- 2026-08-23：macOS 与 Linux 是 V1 正式支持平台并进入构建、单元和关键集成验证；Windows 只尽力保证可编译，不作为发布门槛，不承诺专项运行兼容。
- 2026-08-23：中文注释强制要求仅适用于手写 Go 代码；Protocol Generator 输出保留 `Code generated` 标记与上游 schema 原始注释，不维护中文翻译元数据，也不手工编辑。
- 2026-08-23：按用户最新要求，本变更只负责本地开发、完整验证和本地 Git 提交；`go.mod` 使用本地模块名 `acp-go`，不配置远端、不 push、不创建 PR。未来确定发布仓库后再用独立变更迁移 module path。
- 2026-08-23：用户要求必须把 codex-acp 源码克隆到本地并逐模块对照实现；本项目使用被 Git 忽略的 `.upstream/codex-acp` 固定 1.6.2 快照提交，`UPSTREAM.md`、关键中文注释和移植测试保留源文件/symbol/fixture 对应关系。

# Open questions

- 无。

# Verification expectations

- 规格级：每个直接来源的可执行单元同时映射到完整 target Spec 和至少一个验收 ID；所有 partial/needs-clarification 在确认 Shape 前清零或被明确降级为背景/非目标。
- 单元级：adapter capability、session 状态、event/tool mapper、approval decision、steering queue、stale/early completion 和 unknown event 使用表驱动测试及上游 fixture 移植验证。
- 并发级：在 macOS 与 Linux 使用 `go test -race ./...`，对 steering、cancel、completion、process exit 和 session close 做确定性同步测试，避免仅靠 sleep。
- 集成级：用可控的 JSON-RPC fake app-server 验证进程/协议主链路；另有可选的真实固定版本 `codex app-server` 关键路径检查，凭据缺失时不得伪造通过。
- 工程级：`go test ./...`、`go vet ./...`、格式检查、生成文件 freshness 检查和最小构建通过。
- 平台级：macOS 与 Linux 的构建和关键路径是发布门槛；Windows 仅执行可行的交叉编译/编译检查，失败作为已知限制记录而不改变 V1 正式支持声明。
- 审阅级：抽查/静态检查手写 Go 声明的中文注释覆盖，验证关键并发与异常注释能定位对应 codex-acp symbol/test；检查是否存在重复实现 acp-go-sdk 能力或无实际使用方的预设抽象。
