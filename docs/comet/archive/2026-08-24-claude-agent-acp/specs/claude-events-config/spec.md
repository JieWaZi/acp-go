# Claude events, permissions and configuration complete target specification

## Initialize and content input

- initialize 只声明 V1 已实现的 image、embedded context、load/resume/close、additional directories、MCP HTTP/SSE 和 `_session/steering`；agent name/title/version 稳定且可测试。
- V1 不宣告 auth/logout、session list/fork/delete、providers、goal、file report、typed failure、terminal、elicitation 或 post-v0.70 扩展。
- ACP Text 转为 Claude text content；Image 保留 MIME 与 data/URI；Resource/embedded context 转为带来源标识的文本/文档上下文。Audio 和未知 content block 返回 invalid request，不静默丢弃。

## Messages, usage and plan

- top-level assistant text 映射为 ACP agent message chunk，thinking 映射为 thought chunk；stream delta 与 assembled message 做前缀/身份去重，不能重复最终内容。
- result 的成功、错误、取消与上下文/预算类 stop reason 转为稳定 ACP PromptResponse；usage 使用当前 turn/会话的明确口径并携带可用的 context window 信息。
- Task/Todo 创建、更新与完成维护 per-session task snapshot，并发布基础 ACP plan；V1 不实现 goal extension 或嵌套 subagent transcript。
- 未知事件、未知 content block 与不属于当前 Session/turn 的事件记录有界安全摘要后忽略；核心 V1 事件不能落入未知分支而静默丢失。

## Tool calls

- Claude tool_use 建立稳定 ToolCallID；Read/Edit/Write/Bash/Search/Glob/Task/Todo 等已知工具映射合适标题、kind、位置、原始输入和状态。
- tool_progress 只更新已经发布且仍活动的 ToolCall；tool_result 完成对应调用并保留可表达的文本、图片、diff/路径和错误状态。
- MCP tool 名称与输入/输出作为 generic MCP ToolCall 映射；未知工具仍发布安全 generic ToolCall，不因 mapper 缺项终止 prompt。
- tool cache 按 turn/session identity 清理；迟到进度或结果不得重新打开已完成调用或串到另一 Session。

## Permission handling

- CLI `can_use_tool` control request 关联当前 Session/turn/tool，并调用 ACP `session/requestPermission`；发给客户端的选项只包含固定 v0.70.0 语义下当前请求有效的 allow/reject 选择。
- ACP 选择严格校验后转换为 control response；更新输入、permission updates 或 message 的可选字段只在 upstream 允许时返回。
- request context 取消、Session/turn stale、客户端取消、未知 option、connection 未注入、发送失败、close 或 process exit 全部 fail closed，并确保 CLI 的 control request 最终得到拒绝/取消或随进程关闭解除。
- permission mode 不得在错误/未知配置时扩大权限；危险 bypass 受 upstream/root 约束，默认模式保持可提示的安全边界。

## Configuration

- Session 创建后从 control initialize/固定基线信息建立 model、effort、fast 与 permission mode 选项；只宣告运行时实际返回且 Go 能设置的值。
- model 变化使用 `set_model`，effort/fast 使用固定基线对应的 `apply_flag_settings` 或明确 control request，thinking budget 使用 `set_max_thinking_tokens`，permission mode 使用 `set_permission_mode`。
- config/mode 更新在 Session 内串行；非法 ID/value、CLI 拒绝、query 已关闭或 Session 不存在时返回可诊断错误，不静默成功。
- load/resume 恢复后的实际配置是响应真值；客户端覆盖只在 Session 成功建立后应用。

## Additional directories and client MCP

- additional directories 规范化并去重后映射为独立 `--add-dir` 参数；相对路径按 Session cwd 解析，非法/不可访问目录返回明确错误。
- stdio MCP 保留 name、command、args 和 env；HTTP/SSE MCP 保留 name、URL 与 headers，并编码为 Claude CLI 接受的 `--mcp-config` JSON。
- MCP 名称冲突、协议类型错误、必填字段缺失或不可编码值在启动前失败；secret header/env 不进入日志、错误详情或 fixture。

## Acceptance mapping

- A2：initialize capability 准确性。
- A10-A11：输入、消息、usage 与 plan。
- A12-A13：tool call 与 permission。
- A14-A15：配置、additional directories 和 client MCP。
- A18：mapper/fake CLI/回归验证与中文注释。
