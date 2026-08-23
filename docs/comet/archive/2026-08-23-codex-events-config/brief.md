# Outcome

交付 Codex V1 的事件、ToolCall、三类审批、配置、输入与基础认证映射，并通过 generation 双检与 fail-closed 保证不会把旧 turn 的结果作用于当前 session。

# Scope

- Agent Message、Reasoning、Plan、Token Usage 的 ACP session update 映射与去重。
- Command、File Change、MCP ToolCall 的 started/progress/completed 映射。
- Command、File Change、Permissions 三类 requestPermission 与 Codex response。
- model、reasoning effort、read-only/agent/full-access mode 与 sandbox 映射。
- Text、Image、Resource 输入转换；ChatGPT/API Key 认证。
- unknown method/item 诊断后安全忽略。

# Non-goals

- 不负责 Codex 进程、transport、session/turn/cancel/steering 状态机，由兄弟变更 codex-runtime 负责。
- 不实现 experimental plan delta、Review/Goal、Audio、client-provided MCP server、Gateway Auth。
- 不手写重复协议 DTO，不重写 acp-go-sdk。

# Acceptance examples

- A1：agent message/reasoning 增量按 item ID 关联，completed/fallback 不重复正文。
- A2：plan checklist 和 token usage 映射正确，turn 之间 usage 不泄漏。
- A3：Command/File/MCP 使用稳定 ToolCallID，保留 V1 可表达的标题、位置、diff、raw input/output。
- A4：三类审批只接受本次有效 option；取消、未知、错误、缺 handler、stale generation 一律 fail closed。
- A5：read-only/agent/full-access 映射保持权限边界；未知 mode/model/effort 返回明确错误。
- A6：Text/Image/Resource 转换与上游一致；Audio/非法 union 明确拒绝。
- A7：只声明 api-key/chat-gpt；登录完成通知先订阅后 start，取消会 cancel login，secret 不进入日志/fixture。
- A8：unknown method/item 安全忽略并记录安全摘要，进程和 session 不崩溃。
- A9：全量 test/race/vet/build 通过，手写 Go 声明和关键逻辑有中文注释。

# Constraints and invariants

- 直接对照固定 clone `ba5bcc3d7759250dde9d4d2286a1bec11b363208` 的 `CodexEventHandler.ts`、`CodexToolCallMapper.ts`、`CodexApprovalHandler.ts`、`AgentMode.ts`、`ModelConfigOption.ts`、`CodexAuthMethod.ts` 及 fixtures。
- method 常量、request/notification payload 与 response 只来自 `agents/codex/protocol`。
- Runtime 提供唯一 session/generation 状态；本子变更不得维护第二套 session store。
- permission 在 ACP callback 前后各校验一次 generation，任何异常都不产生正向授权。
- 不在日志、错误、UPSTREAM 或 fixture 写入真实 token/key。

# Decisions

- Router → generation guard → handler/mapper → SDK SessionUpdate/RequestPermission，保持消费方小接口。
- unknown method 保留 `json.RawMessage` 前向兼容，但日志只记录 method/identity/字节长度，不记录 payload。
- File update/move 不自写 patch parser；无法可靠生成 rich diff 时完整保留 raw 数据并记录上游差异。
- 用户补充确认：固定 codex-acp 已有的 mapper、approval、config、content、auth 语义和测试必须直接等价移植；能由 acp-go-sdk 复用的 DTO/update/permission 能力直接调用。只有 Go 语言边界或明确 V1 裁剪允许差异，并须记录到 UPSTREAM.md 与关键中文注释。

# Open questions

- 与 runtime 的共享 Agent/session hook 在集成时以 runtime 已提交的最小接口为准，不复制状态。

# Verification expectations

- 严格 TDD，优先移植固定上游非空 fixtures：agent/reasoning/plan/usage、command/terminal/file/MCP、approval、attachments。
- stale approval 使用显式 barrier，覆盖请求前、等待中与返回后 generation 变化。
- auth 测试注入 opener/account client/waiter，不真实打开浏览器，不泄漏 secret。
