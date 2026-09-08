# Cursor Adapter

启动官方 `cursor-agent acp`，默认命令未找到时尝试 `agent acp`；CLI 入口为 `acp-agent --adapter cursor`。`cursor.Config.CursorPath` 或 CLI 环境变量 `CURSOR_PATH` 指定安装路径。按 [Cursor ACP 文档](https://cursor.com/docs/cli/acp) 安装并登录。

标准 new/load 的 MCP 参数原样传给 Cursor，不另写 .cursor/mcp.json。[官方更新日志](https://cursor.com/docs/cli/changelog) 在 2026-06-22 说明修复了 ACP MCP 服务器被静默丢弃的问题，应使用包含该修复的版本。HTTP/SSE 仍以实际握手为准。每轮新进程和 load 可刷新 MCP 凭据。

`PermissionMode` 支持 default（原生审批）、auto（--auto-review）、full-access（--force）。ACP agent/plan/ask 是工作模式，不能当作权限等级。问答转标准 Elicitation；计划审批的完整正文放入 ToolCall.Content；待办转标准 plan，task/image 通知转工具更新，不伪造完成状态。

协议及业务 fixture 已验证；本机从官方地址下载 2026.09.02-c22c1a3 返回 HTTP 403，因此尚未验证真实 Cursor 进程、登录或模型调用。来源见 [UPSTREAM](../nativeacp/UPSTREAM.md)。
