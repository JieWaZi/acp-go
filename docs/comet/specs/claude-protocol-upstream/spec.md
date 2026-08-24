# Claude protocol and upstream complete target specification

## Fixed baseline

- 行为基线固定为 `agentclientprotocol/claude-agent-acp` tag `v0.70.0`、commit `d0aafb1ca26427285ffaeac8d8a4452fff28e9c3`。
- 协议实现基线固定为该 tag 使用的 `@anthropic-ai/claude-agent-sdk` `0.3.232`；ACP 行为参考其 `@agentclientprotocol/sdk` `1.3.0`，本仓库协议边界继续使用 acp-go-sdk `v0.13.5`。
- `.upstream/claude-agent-acp` 是被 Git 忽略的本地 clone；实现时按固定 tag 读取 `src/acp-agent.ts`、`src/tools.ts`、`src/settings.ts`、相关 tests/fixtures 及 Agent SDK public declarations，不以 moving main 或 README 摘要替代源码证据。

## Wire protocol

- `pkg/claude/protocol` 定义 Claude CLI stream-json 所需的窄类型 envelope，至少区分 system、assistant、user、result、stream_event、tool_progress、control_request、control_response、control_cancel_request 和 keep_alive。
- control subtype 至少覆盖 initialize、can_use_tool、interrupt、set_permission_mode、set_model、set_max_thinking_tokens 和 apply_flag_settings；未纳入 subtype 保留原始 JSON 并走兼容路径。
- envelope 先解析稳定 discriminator，再解码已知 payload；optional/nullable、未知字段和 content union 的语义保持 upstream，不以易碎的单一大结构体吞掉所有消息。
- CLI stdout 按逐行 JSON 解码；非 JSON/未知消息按固定基线记录有界安全摘要并继续，超限帧或不可恢复的 pipe/process 错误结束对应 Session。
- 发往 CLI 的 JSONL 由单写者或等价锁串行化；control request ID 在 Session 内唯一，响应/取消严格关联 pending request，close/exit 会解除全部 pending。

## Traceability

- `pkg/claude/UPSTREAM.md` 记录固定版本、纳入/跳过能力、Claude ACP/Agent SDK symbol 到 Go package/function/test 的映射、Go 等价改写、fixture 来源和升级步骤。
- 代码注释只解释当前 Go 代码的职责、状态、顺序、失败语义和复杂逻辑；版本来源、对照 symbol、fixture 与移植差异不得写进代码，统一由 `pkg/claude/UPSTREAM.md` 承担。
- 不提交 Agent SDK minified bundle、npm tarball 或 upstream clone；只提交为测试裁剪且注明来源的最小 fixture，避免包含凭据、真实路径和用户 transcript。
- upstream 升级必须作为显式变更：先更新固定版本和 fixture，再审计协议/行为 diff，最后修改 Go runtime 与验收证据。

## Acceptance mapping

- A4-A5：JSONL/control 协议与异常兼容。
- A17：固定 upstream 与可审计映射。
- A18：fixture 和完整自动化证据。
