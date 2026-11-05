# 原生 ACP 验证矩阵

在 [源码工作区](../README.md#源码构建) 中运行定向测试。常规测试使用 fake CLI、真实 Go SDK 和冻结原生报文。真实平台、账号和模型单独验收。

| 测试入口 | 覆盖范围 |
| --- | --- |
| `go test ./pkg/agentclient ./pkg/acpclient` | 子进程、冻结启动输入、诊断、原生请求和响应接纳、顺序、配置、取消、观察背压及资源回收 |
| `go test ./cmd/acp-agent ./pkg/acpserver ./pkg/userinput` | 默认及显式适配器选择、错误走 stderr、ACP 接纳与终态回执、问答和退出 |
| `go test ./pkg/standardacp` | Kimi 原生模式、legacy 模型和用量证据，Gemini 冷恢复历史保护，OpenCode 恢复与 Prompt 终态，Grok 逐模型配置 |
| `go test ./pkg/cursor ./pkg/workbuddy ./pkg/qoder` | 厂商启动和握手、配置与交互，WorkBuddy 原生问答及隔离模型探测，Qoder 分区账号、目录与用量证据 |
| `go test ./pkg/codex ./pkg/claude ./pkg/pi` | 各自原生传输、会话、设置、用量、取消、恢复和终态 |

[原生 ACP 来源](../pkg/standardacp/NATIVEACP-UPSTREAM.md)、[Gemini](../pkg/standardacp/GEMINI-UPSTREAM.md)、[OpenCode](../pkg/standardacp/OPENCODE-UPSTREAM.md)、[Grok](../pkg/standardacp/GROK-UPSTREAM.md) 和 [WorkBuddy](../pkg/workbuddy/UPSTREAM.md) 的固定版本资料包含此前适配器的验证记录。旧实现上的真实 CLI 结果只作为历史证据。
