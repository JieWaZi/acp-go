# codex-events-config 上游映射

本目录记录 Go V1 事件、配置与认证组件对固定上游
`agentclientprotocol/codex-acp@ba5bcc3d7759250dde9d4d2286a1bec11b363208`
的逐项来源。测试使用脱敏的内联 wire 样本，不复制包含示例凭据的上游认证
fixture。

## 直接等价移植

| Go 文件/行为 | 上游 symbol | 上游测试或 fixture |
| --- | --- | --- |
| `content.go` Text/Image/Resource 映射 | `CodexAcpClient.ts` `buildPromptItems`、`imageDataUrl`、`formatUriAsLink` | `CodexAcpClient.test.ts` attachments cases；`data/send-attachments-turn-start.json` |
| `config.go` 三种模式 | `AgentMode.ts` `AgentMode.ReadOnly`、`Agent`、`AgentFullAccess` | `session-config-options.test.ts` |
| `config.go` model/effort options | `ModelConfigOption.ts` `createModelConfigOption`、`createReasoningEffortConfigOption`、`findSupportedEffort`；`CodexAcpClient.ts` `createModelId` | `session-config-options.test.ts` |
| `event_router.go` method 分派与 unknown 安全摘要 | `CodexEventHandler.ts` `handleNotification`、`createUpdateEvent` | agent message、reasoning、plan、token usage tests；unknown method 独立 identity/secret-safe 测试 |
| `event_handler.go` agent message/reasoning 去重 | `CodexEventHandler.ts` `handleReasoningDelta`、`handleCompletedReasoningItem` 及 agent-message 分支 | `agent-message-phases.json`、`reasoning-deltas-and-section-break.json`、`reasoning-completed-parts.json` |
| `event_handler.go` plan/usage | `CodexEventHandler.ts` `updatePlan`、`handleTokenUsageUpdated`、`createUsageUpdate` | `plan-checklist-update.json`、`plan-completed-fallback.json`、`token-usage-session-update*.json` |
| `tool_mapper.go` Command/File/MCP | `CodexToolCallMapper.ts` `createCommandExecutionUpdate`、`createCommandExecutionCompleteUpdate`、`createCommandActionEvent`、`createFileChangeUpdate`、`createMcpToolCallUpdate`、`createMcpRawInput`、`createMcpRawOutput`；`CodexEventHandler.ts` `completeCommandExecutionEvent`；`CommandUtils.ts` `stripShellPrefix` | `command-action-events.test.ts`、`terminal-output-events.test.ts`、`terminal-output-completion-fallback.json`、`file-change-events.test.ts` 及其 V1 snapshots |
| `approval.go` 三类审批 | `CodexApprovalHandler.ts` `handleCommandExecution`、`handleFileChange`、`handlePermissionsRequest`、`permissionGrantMetadata` 及 request/response helpers；`ApprovalOptionId.ts` | `approval-events.test.ts`、`approval-command-*.json`、`approval-file-change.json`、`approval-permissions-request.json`；空 changes 精确 meta fixture 与并发 stale barrier 测试 |
| `auth.go` auth methods | `CodexAuthMethod.ts` `getCodexAuthMethods`、API Key/ChatGPT declarations | `initialize.test.ts` auth method cases |
| `auth.go` API Key/ChatGPT 登录 | `CodexAcpClient.ts` `authenticateWithApiKey`、`authenticateWithChatGpt`、`awaitNextLoginCompleted` | `CodexAcpClient.test.ts` API Key/ChatGPT cases |

所有 ACP 内容块、session update、tool call、permission request/response、config option
与 auth method 均直接使用 `github.com/coder/acp-go-sdk@v0.13.5` 的 DTO 和 helper；
Codex wire payload 与 method 均使用 `agents/codex/protocol` 的生成类型和常量。

## 明确的 V1 差异

1. 上游 `buildPromptItems` 静默过滤 `audio`；child spec 要求 V1 显式拒绝，因此
   `content.go` 返回包含 `audio` 身份但不含数据的错误。
2. 上游 TypeScript 借助 npm `diff` 和文件读取重建 update/move rich diff。Go V1
   禁止自写 patch parser，且当前基础依赖没有已批准的等价实现；因此 add/delete
   仍使用 ACP SDK diff DTO，update/move 与 `patchUpdated` 则保留生成协议 typed raw
   changes，不生成可能错误的 old/new text。
3. 固定 snapshot 的生成 schema 未为历史 completed plan item 生成 discriminator 常量；
   `event_handler.go` 仅为兼容上游历史完成项识别 `plan`，稳定实时计划仍只消费
   `turn/plan/updated`，不消费实验性的 `item/plan/delta`。
4. 上游还有 gateway、device-code、dynamic tools 与其他实验分支；它们不属于本 child
   的 V1 acceptance，未在这些组件中实现。
5. Go approval handler 在外部 ACP permission 回调前后各检查 runtime generation，
   并把 callback error/panic、取消、非法 option、缺 handler 全部转换为上游定义的
   fail-closed 响应；这是并发语言边界上的安全加强，不改变正常 option 语义。
6. 认证错误不包装可能回显凭据的上游错误文本，只返回稳定哨兵错误；日志也只记录
   错误类别。该差异用于满足 secret-safe 约束。
