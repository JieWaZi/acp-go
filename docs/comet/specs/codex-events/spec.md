# Codex events complete target specification

## Event routing

- 事件先按 thread/turn 与当前 session 状态关联，再由专用 handler/mapper 转换为 ACP session updates。
- 核心事件使用生成的强类型协议类型；未知事件记录 method/type、相关 identity 与安全摘要后忽略。
- handler 的完成/清理幂等；增量和 completed/fallback 路径不得重复输出最终内容。

## Content updates

- Agent Message 映射为 ACP agent message chunk。
- Reasoning 映射为 ACP thought/reasoning chunk，并保留固定基线支持的 section break/completed fallback 语义。
- 基础 Plan 映射为 ACP plan update；不实现 Review/Goal 的扩展状态机。
- Token Usage 映射为 V1 约定的 ACP update/meta；空值、累计更新和 turn 完成按固定基线处理。

## Tool calls

- Command、File Change、MCP tool 的开始/进度/完成更新使用稳定 ToolCallID 关联。
- Command 尽可能保留标题、命令、路径、状态、终端/输出和原始输入。
- File Change 尽可能保留增加/修改/删除位置、diff 与 raw content；多文件事件不得串线。
- MCP 范围仅为 Codex 已执行 MCP tool event 的 ACP ToolCall 映射，不负责 client-provided MCP server 配置或传输。

## Approval and permission

- Command、File Change、Permissions 三类 app-server request 映射为 ACP `session/requestPermission`。
- 仅暴露固定基线对该请求有效的 allow-once/allow-for-scope/reject/cancel 选项；ACP 选择被严格校验后转换为对应 Codex response。
- ACP 取消、未知选项、超时、handler 缺失、映射异常或 connection 失败均 fail closed：command/file 返回 cancel，permissions 返回无授权/拒绝语义。
- Approval 与 thread/turn/session generation 关联；旧 turn 请求即使迟到也不得批准当前活动。
- cancel/session close/process exit 会解除等待中的 permission 请求并回传关闭语义。

## Acceptance mapping

- A8：消息、reasoning、plan、usage。
- A9：command/file/MCP tool call。
- A10：三类审批、fail closed 与 stale 防护。
- A14：未知事件兼容。
