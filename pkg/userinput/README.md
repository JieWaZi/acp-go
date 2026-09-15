# 统一用户问答

`acpserver.NewWithUserInput` 将同一个 ACP Agent 与问答能力组合。宿主仍只消费标准 `elicitation/create`，不需要理解各 CLI 的提问协议。`acpserver.New` 保留不注入工具的通用 SDK 服务用途。

- Claude 的 AskUserQuestion、Cursor 的 ask_question 保留原生桥；Kimi 顶层 sessionId 规范化后可正确路由。
- 缺少稳定问答工具时注入受管 HTTP MCP `AskUserQuestion`，协议直接复用官方 Go SDK v1.7.0。Pi 通过已有 MCP 工厂执行，不增加 pi-acp 或 MCP 子进程。
- 支持单选、多选、自由文本与自定义答案。结果明确区分 `accept`、`decline`、`cancel`，不把空答案或审批结果当作回答。
- 只监听 loopback；每个会话独立使用随机端口和随机鉴权，拒绝浏览器 Origin。模型可见的 MCP 服务名固定为 `acp_go_user_input`，端点轮换不得改变服务名、工具名或 Schema，避免连续对话破坏提供方前缀缓存。只有当前 Prompt 可以提问；取消立即结束等待，迟到答案不再生效。
- new/load/resume 重新挂载端点，close/delete/进程结束回收服务。Python Kimi 原生问答有缺陷，因此执行前还会确认受管工具目录已经被发现。
- 问答不随权限档位消失。普通 MCP 工具访问仍遵守对应 CLI 的权限门禁，不因为名字相同就给外部工具豁免。

测试覆盖实际 MCP HTTP 鉴权、跨端点模型身份稳定、无活跃执行、迟到回答、选择与自定义答案、取消和超过一分钟等待。跨进程行为由 `scripts/unified-integration` 验证。
