# 上游基线与同步说明

本文档是 V1 Codex Adapter 的上游追溯入口。协议 schema、生成类型和后续运行时行为必须先回到这里列出的固定源码与 fixture 核对，不能以 README 或二手摘要替代源码。

## 固定版本

| 上游 | 固定版本 | 固定提交或解析版本 | 用途 |
| --- | --- | --- | --- |
| `github.com/coder/acp-go-sdk` | v0.13.5 | `0845a3bb9eddda5bfc22a94dd3598c90cb842451` | ACP 连接、分发、取消和扩展基础；本项目不复制其内部协议实现 |
| `github.com/agentclientprotocol/codex-acp` | 1.6.2 | `ba5bcc3d7759250dde9d4d2286a1bec11b363208` | Codex Adapter 行为、状态机、mapper 与 fixture 的直接参考 |
| `@agentclientprotocol/sdk` | 1.4.0 | codex-acp `package-lock.json` 解析值 | TypeScript 参考实现使用的 ACP 协议版本 |
| `@openai/codex` | 0.148.0 | codex-acp 与本仓库 lockfile 的解析值 | app-server 默认稳定 JSON Schema 来源 |
| `quicktype` | 26.0.0 | 本仓库 `tools/protocol/package-lock.json` | Draft-07 JSON Schema→Go 成熟生成器；Apache-2.0 |

本地参考仓库位于项目根目录相对路径 `.upstream/codex-acp`。实现前和每次同步时必须执行：

```sh
git -C .upstream/codex-acp rev-parse HEAD
```

结果必须为 `ba5bcc3d7759250dde9d4d2286a1bec11b363208`。`.upstream/` 由仓库 `.gitignore` 排除，完整 TypeScript 仓库不进入本项目提交。

## Schema 与生成命令

固定输入为 `agents/codex/protocol/schema/codex_app_server_protocol.schemas.json`。它由以下等价命令产生，刻意不传 `--experimental`：

```sh
tools/protocol/node_modules/.bin/codex app-server generate-json-schema --out <temporary-directory>
```

Codex 0.148.0 默认稳定 bundle 的 SHA-256 是 `819fe7b47288cc74da5190743390c8d1faef403f5401a1868b306dac195b1944`。本次固定时连续生成两次得到相同字节和哈希；带 `--experimental` 的 bundle 则有不同哈希，并额外出现 `MockExperimentalMethodParams`、`CurrentTimeReadParams`、`CollaborationModeListParams`、`ProcessSpawnParams`、`ThreadTurnsListParams` 等定义。

受控命令如下：

```sh
# 从 lockfile 中的 Codex 0.148.0 刷新默认稳定 schema，并重新生成 Go 快照
go run ./tools/protocolgen --refresh-schema

# 只从已提交固定输入重新生成 Go 快照
go generate ./agents/codex/protocol

# 不修改文件，逐字节检查已提交快照是否最新
go run ./tools/protocolgen --check
```

生成器通过 `npm ci --ignore-scripts` 使用 `tools/protocol/package-lock.json` 中的精确工具版本；复用现有 `node_modules` 前还会校验 `quicktype --version` 与 `codex --version` 的首行完全匹配固定版本。输出经 `go/format` 规范化，不写入时间和临时路径。`generated_protocol.go` 带 `Code generated` 标记和 Codex 0.148.0 默认稳定 schema 来源，禁止手工修改。

生成或刷新协议需要本机提供 Node.js/npm；Agent 运行时只编译和使用已提交的 Go 快照，不依赖 Node.js、npm、quicktype 或 TypeScript。

## V1 roots 与协议范围

完整默认稳定 bundle 随仓库提交；`protocol.root.json` 只为 V1 运行时需要直接构造或接收的 DTO 增加具名 Go root，不再把完整上游 envelope 交给 quicktype：

- 初始化：`InitializeParams`、`InitializeResponse`；
- thread：start、resume、read、unsubscribe 的 Params/Response；
- turn：start、steer、interrupt 的 Params/Response；
- model/auth/config：`ModelList*`、`ConfigRead*`、`GetAccount*`、`LoginAccount*`、`CancelLoginAccount*`、`LogoutAccountResponse`；
- 审批：Command Execution、File Change、Permissions 的 Params/Response。
- 通知：turn/item 生命周期、消息/reasoning/plan/usage、command/file/MCP 进度、request resolved、compaction、model reroute、warning/error，以及登录流程依赖的 `AccountLoginCompletedNotification`。

根清单来自固定 clone 中 `CodexAppServerClient.ts`、`CodexAcpClient.ts`、`CodexEventHandler.ts` 的实际 imports/dispatcher 与 V1 Specs，而不是重新设计协议。运行时、事件或审批后续需要新的稳定类型时，先把对应 `$ref` 加入 `protocol.root.json`，再生成类型并更新 dispatcher/mapper/test；不能在手写 Go 文件重复声明 DTO。

默认稳定 schema 仍收录少量上游标记为 experimental/unstable 的声明，因此“未传 `--experimental`”不是 V1 public surface 的充分条件。生成器除检查仅由开关产生的哨兵定义外，还通过确定性、逐片段计数的薄适配隐藏 V1 不支持的 experimental capability、Bedrock login 构造项和 plan item 构造常量；完整输入 bundle 保持原样，便于后续升级审计。

## TypeScript → Go 职责映射

| codex-acp 源码或 symbol | Go 文件/职责 | 本次直接核对内容 |
| --- | --- | --- |
| `package.json` 的 `generate-types` | `tools/protocolgen/main.go`、`generate.go` | 上游调用 `codex app-server generate-ts --out src/app-server`；Go 侧改用同版本默认稳定 JSON Schema 再交给固定 quicktype |
| `src/app-server/ClientRequest.ts` | `envelope.go` 的 `ClientRequest` 变体/`Method*` 常量与生成的 V1 Params | initialize、thread、turn、model、account 的 method discriminator 与 wire 字段 |
| `src/app-server/ClientNotification.ts` | `InitializedNotification` | 无 params 的 `initialized` 客户端通知 |
| `src/app-server/ServerRequest.ts` | `envelope.go` 的 `ServerRequest` dispatcher 和生成的三类 approval Params/Response | command/file/permissions requestApproval 的 method、id、params |
| `src/app-server/ServerNotification.ts`、`ServerNotificationEnvelope.ts` | `ServerNotification` dispatcher、具名 Envelope、`UnknownServerNotification` 与生成的通知 Params | turn/item/message/reasoning/plan/usage/tool/unknown-event 路由及 `emittedAtMs` |
| `src/app-server/v2/AccountLoginCompletedNotification.ts`、`src/CodexAcpClient.ts` 登录订阅 | `AccountLoginCompletedNotification`、`AccountLoginCompletedEnvelope` | ChatGPT/API Key login 在 `account/login/start` 前订阅完成通知 |
| `src/app-server/v2/ThreadStartParams.ts`、`ThreadResumeParams.ts` | `ThreadStartParams`、`ThreadResumeParams` | model、cwd、approval、sandbox、config 与恢复参数 |
| `src/app-server/v2/TurnStartParams.ts`、`TurnSteerParams.ts`、`TurnInterruptParams.ts` | 对应生成类型 | required thread/turn identity、输入数组和 steering precondition |
| `src/app-server/v2/ThreadItem.ts`、`UserInput.ts` | `ThreadItem`、`UserInput` 及 discriminator enum | 消息、reasoning、command、file、MCP、Text/Image/Resource 相关 wire union |
| `src/CodexAppServerClient.ts` 的 `initialize`、`turnStart`、`runTurn`、approval request handlers | 后续 JSON-RPC client/runtime；当前提供其所需具名协议类型 | 请求与响应配对、early completion 捕获、stale approval fail-closed 所需 identity 字段 |
| `src/StdUtils.ts` 的 `createJSONRPCReader`/`createJSONRPCWriter` | `internal/codex/appserver_transport.go` | newline 拆帧、malformed 忽略、出站删除 `jsonrpc`；Go 侧额外增加帧长、pending 与 server-request 并发上限 |
| `src/CodexJsonRpcConnection.ts` 的 `startCodexConnection`、`attachLogs` | `internal/codex/process.go`、`executable.go` | 唯一 `codex app-server` 进程、stdin/stdout、退出 dispose、实时 stderr 结构化日志与有界崩溃尾部；可执行文件来源按本项目 V1 的 `CODEX_PATH`→PATH 约束替换 bundled npm fallback |
| `src/CodexAppServerClient.ts` 的 `initialize`、`runTurn`、`awaitTurnCompleted`、`recordTurnCompleted`、`markTurnStale` | `internal/codex/appserver_client.go` | typed request、initialized、response 前 completion 捕获、thread/turn 精确 waiter、stale 清理与 fatal fan-out |
| `src/CodexAcpClient.ts` 的 `newSession`、`resumeSession`、`loadSession`、`closeSession`、`sendPrompt`、`buildPromptItems` | `internal/codex/agent.go`、`session.go`、`prompt.go` | thread start、resume、load 的 resume→read 顺序、unsubscribe、Text/Image/Resource 转换与多轮 prompt |
| `src/CodexAcpServer.ts` 的 `beginSessionOpen`、`sessionOpenCanInstall`、`cleanupStaleSessionOpen`、`trackActivePrompt`、`activePrompt.complete`、`cancelBeforeTurnStarted`、`interruptSessionTurn` | `internal/codex/session.go`、`prompt.go`、`agent.go` | generation/close fence、取消 pending start 后立即释放前台槽位、late turn stale+interrupt、active completion 与 interrupt-once |
| `src/SteeringQueue.ts` 与 `CodexAcpServer.executeOrQueueSteeringRequest`/`performSteeringRequest` | `internal/codex/steering.go` | 每 session 单 consumer FIFO、活动 turn 注入、无活动竞态 fallback 新 turn、单项失败隔离与 idle identity 删除 |
| `src/CodexEventHandler.ts` 的 `handleNotification`、`createItemEvent`、`completeItemEvent`、`completeCommandExecutionEvent` | `internal/codex/event_router.go`、`event_handler.go` | typed method 分派、thread/turn/session identity、消息/reasoning/plan/usage 去重、terminal output fallback/exit 与 unknown 安全摘要 |
| `src/CodexToolCallMapper.ts`、`CommandUtils.ts`、`TerminalOutputMode.ts` | `internal/codex/tool_mapper.go` | Command/File/MCP ToolCall、稳定 ID、终端/输出、raw input/output；file update/move 的有意 raw fallback 见下文 |
| `src/CodexApprovalHandler.ts`、`ApprovalOptionId.ts` | `internal/codex/approval.go` | 三类 permission request/response、common permission meta、amendment option、全异常 fail closed 与 generation 双检 |
| `src/AgentMode.ts`、`ModelConfigOption.ts`、`CodexAcpClient.ts` 的 model/session config helpers | `internal/codex/config.go` | read-only/agent/full-access 权限边界、model/reasoning option、未知选择失败 |
| `src/CodexAcpClient.ts` 的 `buildPromptItems`、`imageDataUrl`、`formatUriAsLink` | `internal/codex/content.go` | ACP SDK Text/Image/Resource 到生成协议 input；Audio 按 V1 明确拒绝 |
| `src/CodexAuthMethod.ts`、`CodexAcpClient.ts` 的 `authenticateWithApiKey`、`authenticateWithChatGpt`、`awaitNextLoginCompleted` | `internal/codex/auth.go` | 仅 API Key/ChatGPT、完成通知先订阅、取消 login、secret-safe 错误边界 |

使用 Codex 0.148.0 `generate-ts` 重新生成后，以下固定 clone 文件均做过逐字节对照且完全一致：`ClientRequest.ts`、`ServerRequest.ts`、`ServerNotification.ts`、`ThreadStartParams.ts`、`ThreadResumeParams.ts`、`TurnStartParams.ts`、`TurnSteerParams.ts`、`TurnInterruptParams.ts` 以及三类 approval Params。

## Fixture → Go 测试映射

| codex-acp fixture/test | 约束的 Go surface/测试 |
| --- | --- |
| `src/__tests__/CodexACPAgent/data/send-attachments-turn-start.json` | `TurnStartParams`、`UserInput` 的 text、URL image、data URL 与 resource 转换 wire |
| `src/__tests__/CodexACPAgent/data/load-session-history.json` | `ThreadReadResponse`、`ThreadItem` 历史消息/tool item；后续 load mapper fixture |
| `approval-command-allow-once.json`、`approval-command-*.json`、`approval-file-change.json`、`approval-permissions-request.json` 及 `approval-events.test.ts` | `approval_test.go` 的三类有效 option/decision、grantRoot、policy amendment、空 permission meta、取消/异常/stale fail closed |
| `agent-message-phases.json`、`reasoning-deltas-and-section-break.json`、`reasoning-completed-parts.json` | `event_handler_test.go` 的 message phase、reasoning section break、delta/completed 去重与 fallback |
| `plan-checklist-update.json`、`plan-completed-fallback.json`、`token-usage-session-update*.json` | plan checklist、usage latest snapshot 与跨 turn 隔离 |
| `terminal-full-flow.json`、`terminal-output-completion-fallback.json`、`terminal-output-events.test.ts` | `tool_mapper_test.go` 的 started→delta→completed 顺序、无 delta 聚合输出回退、`terminal_exit` 与完成重放 exactly-once |
| `command-action-events.test.ts`、`file-change-events.test.ts` 及其 snapshots | parsed Command action、File add/delete rich diff 与 update/move typed raw 保留 |
| `mcp-session.test.ts`、`mcp-tool-in-progress.json`、`mcp-tool-repeated-progress.json`、`mcp-tool-completed-with-logs.json` | MCP title、稳定 ToolCallID、重复 progress、typed raw input/output 与终态 |
| `session-config-options.test.ts` | `config_test.go` 的三模式安全边界、model/effort 保留/回退与未知选择错误 |
| `initialize.test.ts` 和 `CodexAcpClient.test.ts` API Key/ChatGPT cases | `auth_test.go` 的 V1 method 声明、凭据优先级、subscribe-before-start、取消 login 与 secret-safe 失败 |
| `CodexAcpClient.test.ts` 的 concurrent prompt、early completion、cancel/late-start 用例 | `appserver_client_test.go`、`agent_runtime_test.go` | 跨 session/旧 turn 隔离、response 前 completion、cancel-before-start 后新 Prompt、late interrupt-once、completion-first |
| `session-close.test.ts` 的 stale resume/reopen 与 delayed turn start 用例 | `session_test.go`、`agent_runtime_test.go` | close generation、迟到 open、取消 pending turn/start 与后台 goroutine 回收 |
| `steer-events.test.ts` | `steering_test.go` | startedNewTurn→injected FIFO、unexpected failure 后继续、malformed、bounded close cleanup |
| `process-exit-error.test.ts`、`CodexJsonRpcConnection.attachLogs` | `process_test.go`、`appserver_transport_test.go` | exit code/stderr 尾部、pending fatal fan-out，以及运行中/干净退出的实时 stderr 结构化日志 |

固定 clone 中 `input-server-events.json` 是 0 字节占位文件，不作为回归证据。当前 `protocol_test.go` 锁住 envelope method/params 耦合、typed Item、optional+nullable 三态、tagged discriminator、登录完成通知和审批 union 往返。运行时子变更应直接移植上表非空 fixture 的完整行为断言，而不是在协议生成层重复 mapper 逻辑。

## Go 等价改写与已知边界

- JSON Schema 的 wire 名由 `json` tag 原样保留；一般可选字段使用指针/`omitempty`，required 字段不加 `omitempty`。V1 实际依赖的 optional+nullable `grantRoot`、`strictAutoReview` 使用 `OptionalNullable[T]` 保持 absent/null/value 三态，并以行为测试约束逐字节往返。
- quicktype 仍负责成熟的 schema→Go DTO/枚举/标量 union 生成。它无法忠实表达对象型 envelope union，因此完整 `ClientRequest`/`ServerRequest`/`ServerNotification` 不进入 root；薄 `envelope.go` 只复用生成 Params，提供封闭变体、集中 `Method*` 常量和 method-first dispatcher。已知方法不会退化到字段并集，未知通知才以 `json.RawMessage` 前向保留。
- 上游本来定义为开放 `JsonValue`/开放 schema 的 DTO 字段使用 `json.RawMessage`（以及对应 map/slice），不让 `interface{}` 在解码时把整数改写成浮点数；`ItemStartedNotification.Item` 和 `ItemCompletedNotification.Item` 明确为 `ThreadItem`，核心通知 payload 不退化。
- 空 object response 生成 `map[string]json.RawMessage`；JSON-RPC 层仍按对应 method 的具名响应职责配对。
- 外层 ACP JSON-RPC framing、dispatch、prompt context cancel、loader 与 extension 继续直接使用 `github.com/coder/acp-go-sdk` v0.13.5；没有第二套 ACP RPC。Codex 内层必须按 `StdUtils.ts` 自建薄 NDJSON 边界，因为 app-server wire 不带 `jsonrpc`。
- 生成代码保留纳入 V1 声明的上游英文文档；被明确排除的 experimental 构造项及其专属说明由可审计薄适配一并移除，不增加逐字段中文翻译。中文注释规范仅适用于手写 Go。
- `codex-acp` npm 发布物可回退 bundled `@openai/codex`；本项目按用户和父规格只使用用户预装 Codex，显式 `CODEX_PATH` 无效时禁止 PATH 回退，空值才查询 PATH，并以 0.148.0 为告警基线。
- TypeScript `createJSONRPCReader` 和 `SteeringQueue` 使用动态字符串/无界数组；Go 等价实现保持相同顺序与结果语义，但增加 8 MiB 单帧、16 个 server request、64 个每-session pending steering 和有界 stderr，超限按稳定 fatal/RequestError 失败。
- TypeScript 的 early-completion 切换依赖 JavaScript 单事件循环；Go 在同一 mutex 临界区原子执行“查找捕获→安装精确 waiter”，避免 goroutine 在两步间丢通知。
- 固定 TS `InitializeCapabilities` 发送 `experimentalApi: true`；已合并的默认稳定 Go protocol 按 V1 决策有意隐藏该 experimental 字段，因此 runtime 只用生成类型发送 `requestAttestation: false`，不手写重复 DTO 绕过 protocol 边界。
- acp-go-sdk v0.13.5 的 `NewAgentSideConnection` 会立即启动 receive goroutine，之后调用可选 `SetLogger` 存在并发读写；本项目保持 SDK 默认 stderr logger，绝不调用 setter。`connectionBinder` 使用 ready channel 作为 prompt/event barrier。
- Go runtime context 在启动成功后由 `Agent.Close` 单独拥有，不继续继承 construction/Serve context 的取消；这样 acpserver 可在其独立有界清理窗口内先关闭 stdin、回收唯一进程，避免 `exec.CommandContext` 把正常信号退出误报为 app-server 异常。
- 固定 upstream `attachLogs` 会记录 stdin、stdout 与 stderr 数据块；Go 只等价移植 stderr 诊断，并同时写入有界崩溃尾部与组合根注入的结构化 Logger。stdin/stdout 是协议流，可能包含 prompt、响应和认证数据，故有意不记录，也绝不污染外层 ACP stdout。
- 固定 upstream 在 prompt `finally` 中执行 `activePrompt.complete()`，所以 cancel-before-start 返回后立即允许后续 prompt；底层 `sendPromptPromise` 仍继续观察迟到 turn/start 并只中断一次。Go 把前台活动槽位与 `RunTurn` 后台身份分离来保持同一语义；session/Adapter close 另以独立 context 取消 pending RPC 并等待 goroutine，因为 Go 请求可取消且必须显式回收，而 TypeScript Promise 本身不可取消。
- event/config/auth/approval 组件直接使用生成协议 method/Params 和 `github.com/coder/acp-go-sdk@v0.13.5` 的 ContentBlock、SessionUpdate、ToolCall、PermissionOption、AuthMethod、ConfigOption DTO；没有复制 ACP 或 Codex wire DTO。
- 上游 file update/move 依赖 npm `diff` 与文件读取重建 rich diff。Go V1 不自写 patch parser：add/delete 直接使用 SDK diff DTO，update/move 与 patchUpdated 保留生成协议 typed raw changes。
- 上游静默过滤 Audio；V1 未声明 Audio，因此 `content.go` 明确返回请求错误。固定 schema 没有历史 completed plan 的生成常量，兼容分支只识别其 discriminator，稳定计划仍消费 `turn/plan/updated`。
- Go approval 在外部 permission callback 前后读取 runtime generation，并把 error/panic/取消/非法 option/缺 handler 统一 fail closed；空 common permission changes 与上游一致，省略 `permission` meta。
- unknown method 只从 raw params 提取 string thread/turn identity，并连同当前 ACP session、method、payload bytes 记录安全摘要；其他 payload 不进入日志。认证错误同样不包装可能回显凭据的上游详情。

## 明确跳过

- 任何只有 `--experimental` 才出现的 schema 定义、方法和字段，以及默认 bundle 中 V1 不支持的 experimental capability/variant 构造项；
- codex-acp 的 Review、Goal、Session List 管理扩展、client-provided MCP server、Gateway Auth、Audio/realtime、多 Agent 协作 UI 等父 V1 Non-goals；父 V1 runtime 不实现也不宣告这些行为，即使默认稳定 envelope 为前向兼容带出了部分 DTO；
- 完整 codex-acp vendoring、TypeScript 构建产物和 ACP SDK 内部协议实现；
- event/tool/approval/config/content/auth 作为独立小接口组件实现；runtime composition root 只负责生命周期与 identity-aware 薄接线，不复制其 DTO、mapper 或 ACP 请求实现。

## 增量同步步骤

1. 在独立变更中更新 `.upstream/codex-acp`，记录新 tag/commit，并直接阅读受影响 source、symbol、test 和 fixture。
2. 更新 `tools/protocol/package.json` 中 `@openai/codex` 精确版本，重新生成 lockfile；同时更新本文版本表。
3. 运行 `go run ./tools/protocolgen --refresh-schema`。另生成一次带 `--experimental` 的临时 bundle，审查默认/实验边界，不把实验差异合入 V1。
4. 对照 `CodexAppServerClient.ts`、`CodexAcpClient.ts`、`CodexEventHandler.ts` 实际 imports/dispatcher 更新 `protocol.root.json` 和 `envelope.go` 的集中方法清单；运行 `go generate ./agents/codex/protocol`。
5. 先修协议 round-trip/freshness 测试，再修改 runtime、mapper、state 与移植 fixture；记录每个有意 Go 差异。
6. 运行两次生成差异检查、`go run ./tools/protocolgen --check`、`go test ./...` 与 `go vet ./...` 后再提交。

## 同步记录

- 2026-08-23：建立 V1 初始固定点：acp-go-sdk v0.13.5、codex-acp 1.6.2、ACP TS SDK 1.4.0、Codex 0.148.0、quicktype 26.0.0；固定默认稳定 schema 与 Go 快照，并完成关键 generated TS、source imports 和 fixture 的直接对照。
- 2026-08-23：收窄 V1 roots，移除 quicktype 完整 envelope 字段并集；加入封闭 typed envelope dispatcher、集中方法常量、approval nullable 三态、typed Item 和登录完成通知，同时把 0 字节占位 fixture 从回归证据中移除。
- 2026-08-23：接入 Codex runtime：用户预装可执行文件与版本探测、唯一 app-server、有界无 `jsonrpc` NDJSON、initialize、session generation/close fence、multi-turn/early/stale completion、cancel/late interrupt、FIFO steering、进程 fatal fan-out 和 SDK connection ready barrier。
- 2026-08-23：补齐固定 upstream 的实时 stderr 与 pending-start 生命周期：stderr 同时进入有界崩溃尾部和结构化 Logger；cancel-before-start 立即释放前台槽位但保留迟到 observer；session/Adapter close 取消并回收 pending `RunTurn`。
- 2026-08-23：按 codex-acp `ba5bcc3` 等价移植 event/tool/approval/config/content/auth 组件；补齐 terminal completion fallback/exit、unknown identity 安全摘要、空 permission meta 和并发 stale approval 证据，明确最终 runtime wiring 与 file update/move raw fallback 边界。
