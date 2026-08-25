# Codex ACP Go V1 测试矩阵

V1 验收分为三层：手写 fixture 覆盖协议异常和竞态；最近一次成功真实验收自动保存的双向 app-server fixture 覆盖真实数据回放；显式真实测试使用本机已登录的 Codex。默认测试不读取凭据、不请求模型、无模型费用。模型没有触发某类事件不能代替协议层的确定性测试。

## 能力覆盖

| V1 能力 | 默认确定性测试 | 真实 Codex 测试 | 说明 |
| --- | --- | --- | --- |
| 默认/显式 Codex Adapter、ACP initialize | `TestRunStartsDefaultAndExplicitCodex`、`TestRunProductionCompositionSessionFlow` | `TestRunRealCodexSmoke`、`TestRunRealCodexV1Capabilities` | 检查协议版本、能力、认证方式与 steering meta |
| Codex 路径、前置参数、完整环境、版本探测、唯一 app-server、退出清理 | `executable_test.go`、`process_test.go`、`appserver_transport_test.go` | 两个真实测试都会启动本机 app-server | 前置参数和完整环境同时用于探测与长期进程；录制 fixture 的文件名和内容不绑定本机 Codex 版本 |
| New/Load/Resume/Close Session 与历史回放 | `agent_runtime_test.go`、`agent_wiring_test.go` | `load_resume_and_history` | 真实测试关闭、加载历史、再次关闭并恢复后继续 Prompt |
| Additional directories、trusted roots 与 Skills | `workspace_test.go`、`TestAppServerClientRefreshSkillsUpdatesRootsAndForcesReload`、`TestAgentSessionConfigurationFlowsIntoTurnStart` | 未单独执行真实 Skill 场景 | 覆盖绝对路径/去重、Session config、workspaceWrite roots、extra roots 与强制扫描 |
| 多轮 Prompt、Agent Message 与 delta | `TestRunProductionCompositionSessionFlow`、`event_handler_test.go` | `message_delta_thinking_usage` 及后续多轮场景 | fake 组合测试强制发送两段 message delta |
| Reasoning/Thinking、Plan、Token/Prompt Usage | `TestRunProductionCompositionSessionFlow`、`TestEventRouterMapsPlanAndLatestUsage`、`TestAgentSessionConfigurationFlowsIntoTurnStart` | `message_delta_thinking_usage`、`plan_and_command_tool` | 同时覆盖 session usage_update 与 PromptResponse 的 input/cache/output/thought/total 明细 |
| Command Tool start/delta/completed | `TestRunProductionCompositionSessionFlow`、`tool_mapper_test.go` | `plan_and_command_tool` | 检查同一 ToolCallID 和完成状态 |
| File Change Tool 与 turn 聚合 diff | `TestEventRouterMapsFileChangesToStandardDiff`、`TestEventRouterPreservesUnverifiableFileUpdate`、`file_diff_test.go`、`TestTurnDiffUpdatedNotificationIsStronglyTyped` | `file_change_tool` | add/delete/update/move 生成标准 ACP diff；坏补丁安全降级；`turn/diff/updated` 只做强类型识别；真实测试只修改 `t.TempDir()` |
| Web Search 与 Image View Tool | `TestEventRouterMapsWebSearchAndImageView`、`TestAgentLoadReplaysHistoryThroughExistingMappers` | 不依赖模型随机触发 | 与 upstream 一致使用 search/read、结构化 rawInput、ResourceLink，并保证 Image View 只发一张 completed 工具卡片 |
| MCP 配置、启动状态与 Tool Call | `TestAgentSessionMCPConfigMatchesCodexACP`、`TestCodexMCPServerConfigRejectsUnsupportedTransports`、`TestEventRouterMapsMCPProgressAndCompletion` | 不自动调用用户 MCP | 覆盖 stdio/HTTP、同名配置保护、失败状态与既有 MCP 工具事件；SSE/ACP transport 明确拒绝 |
| MCP Elicitation 与结构化用户输入 | `TestMCPServerElicitationUsesACPAndCompletesURL`、`TestToolRequestUserInputUsesACPForm` | 不自动触发外部交互 | 覆盖 form/url 能力路由、URL complete、选项/Other 答案和 fail-closed |
| Command/File/Permissions 三类审批与 fail-closed | `approval_test.go`、`agent_runtime_test.go`、`process_test.go` | `approval_allow_once` 验证真实 allow_once 往返 | 异常、取消、stale、缺 handler 必须由确定性测试覆盖 |
| Cancel/Interrupt 与取消后恢复 | `agent_runtime_test.go` | `cancel_and_recovery` | 真实测试取消活动 `sleep` 工具，再执行后续 turn |
| Steering、FIFO 与单项失败隔离 | `steering_test.go` | `steering_active_turn` | FIFO/容量/失败隔离不依赖模型时序 |
| Model、Reasoning Effort、Sandbox/Agent Mode | `config_test.go`、`agent_wiring_test.go` | 真实测试实际设置当前 model、最高可用 effort 和 mode | 未知选择必须稳定失败 |
| Text、Image、Resource、ResourceLink 输入 | `content_test.go`、`prompt_test.go` | `image_resource_and_resource_link` | 图片由测试生成；资源文件只位于临时目录；Audio 是 V1 非目标 |
| ChatGPT/API Key 认证与凭据安全 | `auth_test.go`、`agent_wiring_test.go` | initialize 检查声明；真实 Prompt 验证当前本机登录态 | 真实测试不自动 logout、不读取或打印密钥 |
| early completion、stale turn/approval、unknown event | `appserver_client_test.go`、`agent_runtime_test.go`、`event_handler_test.go` | 不依赖模型随机触发 | 这些竞态必须使用屏障和固定 identity 验证；upstream 明确忽略的 hook 通知不记录为未知能力 |
| Session 缺失错误 | `TestAgentReturnsResourceNotFoundForMissingSession`、`agent_wiring_test.go` | 不适用 | Prompt、配置和 steering 使用 ACP `ResourceNotFound`，恢复透传 app-server `-32002` |
| Schema freshness、上游固定点、中文注释 | `protocolgen` 测试、`TestHandwrittenGoDeclarationsHaveChineseComments` | 不适用 | 生成代码豁免中文注释但必须通过 freshness |

## 运行方式

默认完整测试：

```sh
go test ./... -count=1 -timeout=300s
go test -race -p=1 ./... -count=1 -timeout=600s
go vet ./...
go run ./tools/protocolgen --check
```

存在 `cmd/acp-agent/testdata/real_codex_v1.jsonl` 时，默认测试会自动执行 `TestReplayRecordedRealCodexV1Capabilities`，经生产协议链路回放最近一次成功真实验收的数据；fixture 尚未生成时该用例会明确跳过。

最小真实测试，只产生一次模型请求：

```sh
ACP_GO_REAL_CODEX=1 go test ./cmd/acp-agent \
  -run '^TestRunRealCodexSmoke$' -count=1 -v -timeout=240s
```

V1 真实能力测试会产生多次模型请求、执行受控 `sleep`/`printf`，并只在测试临时目录创建文件：

```sh
ACP_GO_REAL_CODEX_V1=1 go test ./cmd/acp-agent \
  -run '^TestRunRealCodexV1Capabilities$' -count=1 -v -timeout=15m
```

该命令只有在全部 V1 场景和进程清理都成功后，才会自动、原子地更新 `cmd/acp-agent/testdata/real_codex_v1.jsonl`。记录的是 app-server 双向 NDJSON；临时路径、用户目录、运行时版本、动态协议 ID 和凭据类字段会先归一化或脱敏。以后执行普通 `go test ./...` 即使用这份数据回放，不会再次调用真实 Codex。再次显式设置 `ACP_GO_REAL_CODEX_V1=1` 才会重新请求模型并刷新 fixture。

运行真实测试前应先用 `codex login status` 和一次直接 `codex exec` 确认本机凭据有效。真实测试不会启动登录流程。
