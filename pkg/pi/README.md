# Pi Adapter

调用链是 **ACP 客户端 → acp-go → `pi --mode rpc`**。acp-go 参照 MIT 开源项目 [svkozak/pi-acp](https://github.com/svkozak/pi-acp) 用 Go 移植协议适配；不安装、不发现、不启动 `pi-acp`。

用户只需安装 Pi（已验证 0.84.1，Node.js >=22.19），在 Pi 原生终端配置模型提供方和登录：

```sh
npm install -g @earendil-works/pi-coding-agent@0.84.1
pi
PI_PATH=/absolute/path/to/pi PI_MCP_MODULE_PATH=/absolute/path/to/bridge.mjs acp-agent --adapter pi
```

`PI_PATH` / `pi.Config.PiPath` 可省略，默认从完整环境 PATH 发现 `pi`。Ally 的可执行文件填写 `pi` 或其绝对路径。`PrefixArgs` 放在固定 RPC 参数之前；`Environment` 是完整子进程环境，nil 继承宿主。不自动安装或登录。

宿主必须为 `pi.Config.MCPModulePath` 提供一个可读取的绝对文件路径；独立运行 `acp-agent` 时使用 `PI_MCP_MODULE_PATH=/absolute/path/to/bridge.mjs`。缺失、相对路径或文件不存在会在构造 Agent 时明确失败；导入或注册失败会在会话启动时明确失败，不接受 Pi 单独返回的 `get_state` 成功作为扩展就绪证据。bridge 是供 Pi 官方扩展加载器导入的单文件模块，必须导出 `createMcpAdapter`、`classifyAction`、`createReviewSubject` 和 `completeSimple`。所有权限档和无用户 MCP 的会话都要求它，因为权限审查与受管问答也使用这份组合入口。宿主负责固定 bridge 与 Pi 的兼容版本；Ally 在 `desktop/pi-bridge` 固定依赖并随安装包提供模块，其他宿主需自行提供等价文件。

协议能力包括文本/思考流、图片/嵌入上下文、工具状态、文件差异、Bash 终端输出/退出码、取消、会话 new/load/resume/list/close/delete、真实模型与思考配置。历史直接使用 Pi 原生 JSONL；load 回放消息，resume 不回放。每个活跃会话拥有独立 Pi 进程和 MCP 快照，恢复先关闭旧进程，支持更新凭据。`agent_settled` 才结束提示；中间 `agent_end` 不结束重试或自动压缩。排队请求可取消。

对照 pi-acp 提供 `/compact`、`/autocompact`、`/export`、`/session`、`/name`、`/steering`、`/follow-up`、`/changelog`，技能和提示模板交给 Pi 原生加载。与参考版本一致，命令目录不发布依赖交互终端的扩展 slash 命令。Pi 的项目资源信任规则仍由 Pi 管理；不要假定未信任工作区会自动加载扩展。

MCP 直接复用 `pi-mcp-adapter@2.32.1` 公开配置工厂，支持 STDIO、Streamable HTTP、SSE。acp-go 只在 Go 模板中保存会话扩展源码，运行时生成权限为 0600 的临时 `extension.ts`，并让 Pi 从宿主的 bridge 路径导入固定版本的上游实现；会话关闭后删除临时文件，不删除宿主模块。不合并或改写用户配置，参数/环境/请求头保持字面量。acp-go 的构建与测试不需要 npm。

`PermissionMode` 提供 `default`、`auto`、`full-access` 三档。默认档使用上游 MCP 审批和官方工具 hook；自动档复用固定的开源风险分类器，使用当前所选模型，判断不明或失败时转人工；完全访问跳过本扩展的工具审批，仍受操作系统和外部服务限制。工作区外读取会请求确认，不把任意 MCP readOnlyHint 当成授权。

宿主需要完整问答时使用 `acpserver.NewWithUserInput`。Go 进程内提供受管 AskUserQuestion，Pi 第一轮就能调用，问题始终等待真实用户。普通 select/input/editor 转为 ACP form；只有确认和 MCP 审批选项进入 RequestPermission。取消、跳过与回答分别处理。没有 pi-acp 或额外问答进程，不提供独立操作系统沙箱。

新增真实进程回归见 `scripts/unified-integration/`：三档权限、所选模型参与审查、失败转人工、外部 MCP 副作用、问答回填与一分钟以上等待。原始会话/文件/终端/三传输回归保留在 `scripts/pi-integration/`。
