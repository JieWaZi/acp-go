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

生成器通过 `npm ci --ignore-scripts` 使用 `tools/protocol/package-lock.json` 中的精确工具版本。输出经 `go/format` 规范化，不写入时间和临时路径。`generated_protocol.go` 带 `Code generated` 标记和 Codex 0.148.0 默认稳定 schema 来源，禁止手工修改。

生成或刷新协议需要本机提供 Node.js/npm；Agent 运行时只编译和使用已提交的 Go 快照，不依赖 Node.js、npm、quicktype 或 TypeScript。

## V1 roots 与协议范围

完整默认稳定 bundle 随仓库提交；`protocol.root.json` 只为 V1 运行时需要直接构造或接收的类型增加具名 Go root，并让 envelope 继续传递默认稳定通知/请求：

- envelope：`ClientRequest`、`ClientNotification`、`ServerRequest`、`ServerNotification`；
- 初始化：`InitializeParams`、`InitializeResponse`；
- thread：start、resume、read、unsubscribe 的 Params/Response；
- turn：start、steer、interrupt 的 Params/Response；
- model/auth/config：`ModelList*`、`ConfigRead*`、`GetAccount*`、`LoginAccount*`、`CancelLoginAccount*`、`LogoutAccountResponse`；
- 审批：Command Execution、File Change、Permissions 的 Params/Response。

根清单来自固定 clone 中 `CodexAppServerClient.ts` 的实际 imports 与 V1 Specs，而不是重新设计协议。运行时、事件或审批后续需要新的稳定类型时，先把对应 `$ref` 加入 `protocol.root.json`，再生成类型并更新 mapper/test；不能在手写 Go 文件重复声明 DTO。

默认稳定 schema 中上游仍有文字标为 `EXPERIMENTAL` 的历史注释；是否属于生成范围以 Codex 0.148.0 `generate-json-schema` 默认输出为唯一机械边界。本项目没有传 `--experimental`，也没有引入仅在该开关下出现的定义、方法或字段。

## TypeScript → Go 职责映射

| codex-acp 源码或 symbol | Go 文件/职责 | 本次直接核对内容 |
| --- | --- | --- |
| `package.json` 的 `generate-types` | `tools/protocolgen/main.go`、`generate.go` | 上游调用 `codex app-server generate-ts --out src/app-server`；Go 侧改用同版本默认稳定 JSON Schema 再交给固定 quicktype |
| `src/app-server/ClientRequest.ts` | `generated_protocol.go` 的 `ClientRequest`、`ClientRequestMethod` 与具名 V1 Params | initialize、thread、turn、model、account 的 method discriminator 与 wire 字段 |
| `src/app-server/ServerRequest.ts` | `ServerRequest` 和三类 approval Params/Response | command/file/permissions requestApproval 的 method、id、params |
| `src/app-server/ServerNotification.ts` | `ServerNotification`、`NotificationMethod`、通知 Params | turn/item/message/reasoning/plan/usage/tool/unknown-event 路由的 wire 输入 |
| `src/app-server/v2/ThreadStartParams.ts`、`ThreadResumeParams.ts` | `ThreadStartParams`、`ThreadResumeParams` | model、cwd、approval、sandbox、config 与恢复参数 |
| `src/app-server/v2/TurnStartParams.ts`、`TurnSteerParams.ts`、`TurnInterruptParams.ts` | 对应生成类型 | required thread/turn identity、输入数组和 steering precondition |
| `src/app-server/v2/ThreadItem.ts`、`UserInput.ts` | `ThreadItem`、`UserInput` 及 discriminator enum | 消息、reasoning、command、file、MCP、Text/Image/Resource 相关 wire union |
| `src/CodexAppServerClient.ts` 的 `initialize`、`turnStart`、`runTurn`、approval request handlers | 后续 JSON-RPC client/runtime；当前提供其所需具名协议类型 | 请求与响应配对、early completion 捕获、stale approval fail-closed 所需 identity 字段 |
| `src/CodexApprovalHandler.ts` 的 `handleCommandExecution`、`handleFileChange`、`handlePermissionsRequest` | 后续 approval mapper；当前生成三类 Params/Response | 三类有效 decision union、失败时 cancel/reject 的 wire 形状 |

使用 Codex 0.148.0 `generate-ts` 重新生成后，以下固定 clone 文件均做过逐字节对照且完全一致：`ClientRequest.ts`、`ServerRequest.ts`、`ServerNotification.ts`、`ThreadStartParams.ts`、`ThreadResumeParams.ts`、`TurnStartParams.ts`、`TurnSteerParams.ts`、`TurnInterruptParams.ts` 以及三类 approval Params。

## Fixture → Go 测试映射

| codex-acp fixture | 约束的 Go surface/后续测试 |
| --- | --- |
| `src/__tests__/CodexACPAgent/data/input-server-events.json` | JSON-RPC request/response envelope 与 method/params 保真 |
| `src/__tests__/CodexACPAgent/data/send-attachments-turn-start.json` | `TurnStartParams`、`UserInput` 的 text、URL image、data URL 与 resource 转换 wire |
| `src/__tests__/CodexACPAgent/data/load-session-history.json` | `ThreadReadResponse`、`ThreadItem` 历史消息/tool item；后续 load mapper fixture |
| `src/__tests__/CodexACPAgent/data/approval-command-allow-once.json` | Command approval decision union 与 raw params |
| `src/__tests__/CodexACPAgent/data/approval-file-change.json` | File Change approval Params/Response 与 grantRoot nullable |
| `src/__tests__/CodexACPAgent/data/approval-permissions-request.json` | Permissions profile、session/turn grant 与 reject decision |
| `src/__tests__/CodexACPAgent/data/agent-message-phases.json` | notification/item discriminator 与 commentary/final phase 字段 |

当前 `protocol_test.go` 先锁住生成层的 optional/required、tagged discriminator 和审批 union 往返。运行时子变更应直接移植上表 fixture 的完整行为断言，而不是在协议生成层重复 mapper 逻辑。

## Go 等价改写与已知边界

- JSON Schema 的 wire 名由 `json` tag 原样保留；可选字段使用指针/`omitempty`，required 字段不加 `omitempty`，nullable 标量或 union 使用指针或 quicktype union wrapper。
- quicktype 会把对象型 tagged union 的公共 envelope 表示为 discriminator enum 加字段并集；method/type 及各变体 wire 字段可以无损往返。为了保持运行时可读和可构造，V1 实际使用的 Params/Response 另以直接 `$ref` 生成具名强类型。此差异由 round-trip 测试约束，不在手写代码中重造通用 union 生成器。
- 任意 JSON Schema 值保持为 `interface{}`/map；这对应上游 `JsonValue` 或开放 schema，不擅自收窄。
- 空 object response 生成 `map[string]interface{}`；JSON-RPC 层仍按对应 method 的具名响应职责配对。
- 生成代码保留上游 schema 英文原始文档，不增加逐字段中文翻译；中文注释规范仅适用于手写 Go。

## 明确跳过

- 任何只有 `--experimental` 才出现的 schema 定义、方法和字段；
- codex-acp 的 Review、Goal、Session List 管理扩展、client-provided MCP server、Gateway Auth、Audio/realtime、多 Agent 协作 UI 等父 V1 Non-goals；父 V1 runtime 不实现也不宣告这些行为，即使默认稳定 envelope 为前向兼容带出了部分 DTO；
- 完整 codex-acp vendoring、TypeScript 构建产物和 ACP SDK 内部协议实现；
- 本子变更不实现子进程、session/turn 状态、event mapper、approval handler 或配置逻辑，这些由后续子变更消费本协议包。

## 增量同步步骤

1. 在独立变更中更新 `.upstream/codex-acp`，记录新 tag/commit，并直接阅读受影响 source、symbol、test 和 fixture。
2. 更新 `tools/protocol/package.json` 中 `@openai/codex` 精确版本，重新生成 lockfile；同时更新本文版本表。
3. 运行 `go run ./tools/protocolgen --refresh-schema`。另生成一次带 `--experimental` 的临时 bundle，审查默认/实验边界，不把实验差异合入 V1。
4. 对照 `CodexAppServerClient.ts` 实际 imports 更新 `protocol.root.json`；运行 `go generate ./agents/codex/protocol`。
5. 先修协议 round-trip/freshness 测试，再修改 runtime、mapper、state 与移植 fixture；记录每个有意 Go 差异。
6. 运行两次生成差异检查、`go run ./tools/protocolgen --check`、`go test ./...` 与 `go vet ./...` 后再提交。

## 同步记录

- 2026-08-23：建立 V1 初始固定点：acp-go-sdk v0.13.5、codex-acp 1.6.2、ACP TS SDK 1.4.0、Codex 0.148.0、quicktype 26.0.0；固定默认稳定 schema 与 Go 快照，并完成关键 generated TS、source imports 和 fixture 的直接对照。
