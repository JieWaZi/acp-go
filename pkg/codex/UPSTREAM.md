# Codex 上游基线与同步说明

本文档是 Codex Adapter 唯一的上游追溯入口。协议声明、生成类型和运行时行为必须先回到这里列出的固定源码与 fixture 核对，不能以 README 或二手摘要替代源码。Claude 的独立基线见 [`../claude/UPSTREAM.md`](../claude/UPSTREAM.md)。

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

## 原生客户端

当前使用 `client.go`、`session.go`、`turn.go` 和 `management.go` 实现 App Server 连接、会话、运行和管理。生成协议与固定 schema 保持同一来源。执行与 SDK 投影由当前实现的 fake App Server 和独立命令测试验证。

## Schema 与生成命令

固定输入为 `pkg/codex/protocol/schema/codex_app_server_protocol.schemas.json`。它由以下等价命令产生，刻意不传 `--experimental`：

```sh
tools/protocol/node_modules/.bin/codex app-server generate-json-schema --out <temporary-directory>
```

Codex 0.148.0 默认稳定 bundle 的 SHA-256 是 `819fe7b47288cc74da5190743390c8d1faef403f5401a1868b306dac195b1944`。本次固定时连续生成两次得到相同字节和哈希；带 `--experimental` 的 bundle 则有不同哈希，并额外出现 `MockExperimentalMethodParams`、`CurrentTimeReadParams`、`CollaborationModeListParams`、`ProcessSpawnParams`、`ThreadTurnsListParams` 等定义。

受控命令如下：

```sh
# 从 lockfile 中的 Codex 0.148.0 刷新默认稳定 schema，并重新生成 Go 快照
go run ./tools/protocolgen --refresh-schema

# 只从已提交固定输入重新生成 Go 快照
go generate ./pkg/codex/protocol

# 不修改文件，逐字节检查已提交快照是否最新
go run ./tools/protocolgen --check
```

生成器通过 `npm ci --ignore-scripts` 使用 `tools/protocol/package-lock.json` 中的精确工具版本；复用现有 `node_modules` 前还会校验 `quicktype --version` 与 `codex --version` 的首行完全匹配固定版本。输出经 `go/format` 规范化，不写入时间和临时路径。`generated_protocol.go` 带 `Code generated` 标记和 Codex 0.148.0 默认稳定 schema 来源，禁止手工修改。

生成或刷新协议需要本机提供 Node.js/npm；Agent 运行时只编译和使用已提交的 Go 快照，不依赖 Node.js、npm、quicktype 或 TypeScript。

## V1 roots 与协议范围

完整默认稳定 bundle 随仓库提交；`protocol.root.json` 只为 V1 运行时需要直接构造或接收的 DTO 增加具名 Go root，不再把完整上游 envelope 交给 quicktype：

- 初始化：`InitializeParams`、`InitializeResponse`；
- thread：start、resume、fork、compact、read、unsubscribe 的 Params/Response；
- turn：start、steer、interrupt 的 Params/Response；
- model/auth/config/skills：`ModelList*`、`ConfigRead*`、`GetAccount*`、`GetAccountRateLimitsResponse`、`LoginAccount*`、`CancelLoginAccount*`、`LogoutAccountResponse`、`SkillsExtraRootsSet*`、`SkillsList*`；
- 审批与交互：Command Execution、File Change、Permissions、MCP Elicitation、`item/tool/requestUserInput` 的 Params/Response。
- 通知：turn/item 生命周期、已知但不直接转 ACP 的 `turn/diff/updated`、消息/reasoning/plan/usage、command/file/MCP 进度、MCP 启动状态、request resolved、compaction、model reroute、warning/error，以及登录流程依赖的 `AccountLoginCompletedNotification`。

根清单来自固定 clone 中 `CodexAppServerClient.ts`、`CodexAcpClient.ts`、`CodexEventHandler.ts` 的实际 imports/dispatcher 与 V1 Specs，而不是重新设计协议。运行时、事件或审批后续需要新的稳定类型时，先把对应 `$ref` 加入 `protocol.root.json`，再生成类型并更新 dispatcher/mapper/test；不能在手写 Go 文件重复声明 DTO。

默认稳定 schema 仍收录少量上游标记为 experimental/unstable 的声明，因此“未传 `--experimental`”不是 V1 public surface 的充分条件。V1 明确采用其中的 `item/tool/requestUserInput` 以对接 ACP Form Elicitation；生成器只为该请求做窄开放，并继续通过确定性、逐片段计数的薄适配隐藏其余不支持的 experimental capability、Bedrock login 构造项和 plan item 构造常量。完整输入 bundle 保持原样，便于后续升级审计。

## 历史同步记录

- 2026-08-23：建立 V1 初始固定点：acp-go-sdk v0.13.5、codex-acp 1.6.2、ACP TS SDK 1.4.0、Codex 0.148.0、quicktype 26.0.0；固定默认稳定 schema 与 Go 快照，并完成关键 generated TS、source imports 和 fixture 的直接对照。
- 2026-08-23：收窄 V1 roots，移除 quicktype 完整 envelope 字段并集；加入封闭 typed envelope dispatcher、集中方法常量、approval nullable 三态、typed Item 和登录完成通知，同时把 0 字节占位 fixture 从回归证据中移除。
- 2026-08-23：接入 Codex runtime：用户预装可执行文件与版本探测、唯一 app-server、有界无 `jsonrpc` NDJSON、initialize、session generation/close fence、multi-turn/early/stale completion、cancel/late interrupt、FIFO steering、进程 fatal fan-out 和 SDK connection ready barrier。
- 2026-08-23：补齐固定 upstream 的实时 stderr 与 pending-start 生命周期：stderr 同时进入有界崩溃尾部和结构化 Logger；cancel-before-start 立即释放前台槽位但保留迟到 observer；session/Adapter close 取消并回收 pending `RunTurn`。
- 2026-08-23：按 codex-acp `ba5bcc3` 等价移植 event/tool/approval/config/content/auth 组件；补齐 terminal completion fallback/exit、unknown identity 安全摘要、空 permission meta 和并发 stale approval 证据，明确最终 runtime wiring 与 file update/move raw fallback 边界。
- 2026-08-24：runtime composition root 直接复用已验证 event/approval/content 组件：typed 通知按当前 turn generation 路由，三类生成 ServerRequest 通过 SDK connection 请求权限并在缺失/迟到身份时 fail closed；删除 runtime 临时内容 mapper；补足 turn/start response activation barrier 与合成 completion 的 stale identity 回收。
- 2026-08-24：按 `session-close.test.ts` delayed turn/start 修正 Go 可取消调用差异：本地 waiter 与 transport late observer 分离，CloseSession 无需等待 response，迟到 turn 仍经真实 wire stale+interrupt-once；补 ordinary cancel、duplicate response 与 fatal observer 回收测试。
- 2026-08-24：完成 stabilization 生产接线：Initialize/Authenticate/Logout、new/load/resume config、set mode/config、turn payload 和 history replay 直接复用既有组件；增加 SDK stdio 到可控 fake app-server 子进程的组合回归。
- 2026-08-24：按独立审查再对照 upstream，补齐 history command/file/MCP 的精确 `tool_call` 形状、no-active steering 配置继承、取消后有界远端清理和 config/close 线性化；同时加入模型分页资源上限。
- 2026-08-24：stabilization verification repair 薄移植 `TerminalOutputMode.ts` 和 initialize capability 条件：`NO_BROWSER` 非空时隐藏 ChatGPT、只宣告 `steering.supported`、按 session 快照选择 full/legacy terminal key，并用无 shell 派生的 helper-process fake 消除默认 package 并行下的版本探测竞争与 pipe 假挂。
- 2026-08-24：按 `CodexAcpServer.runWithProcessCheck` 与 `process-exit-error.test.ts` 修复 Go `exec.Cmd.Wait`/`StdoutPipe` 竞态：scanner 的 closed-pipe 仅在 `process.FinalError` 确认异常退出时升级为稳定 exit/stderr fatal，并以确定性双 pending 仲裁测试及真实 helper-process 并行压力锁住普通 I/O、超长帧、clean EOF 和主动关闭优先级。
- 2026-08-24：参考固定 codex-acp 补齐 ACP stdio/HTTP MCP 会话配置、同名配置层保护、启动失败展示、MCP form/url Elicitation 与 `item/tool/requestUserInput`；SSE、MCP-over-ACP 和动态 `item/tool/call` 继续保持未声明。
- 2026-08-24：补齐 additional directories 的 trusted/writable roots、`.agents/skills` extra roots 与强制刷新、PromptResponse Usage 和稳定 Session `ResourceNotFound`；新增生成协议 Skills roots，并用手写兼容别名避免 quicktype 命名变化破坏既有导出名称。
- 2026-08-25：把 `turn/diff/updated` 纳入强类型通知但按 upstream 保持无 ACP 输出；参考 `createPatchContent` 补齐 update/move 的当前文件读取、补丁正反应用与标准 ACP old/new diff，坏补丁继续安全降级为 raw change。
