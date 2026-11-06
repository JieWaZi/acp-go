# Pi Adapter

调用链是 **ACP 客户端 → acp-go → `pi --mode rpc`**。acp-go 参照 MIT 开源项目 [svkozak/pi-acp](https://github.com/svkozak/pi-acp) 用 Go 移植协议适配；不安装、不发现、不启动 `pi-acp`。

用户只需安装 Pi（已验证 0.84.1，Node.js >=22.19），在 Pi 原生终端配置模型提供方和登录：

```sh
npm install -g @earendil-works/pi-coding-agent@0.84.1
pi
PI_PATH=/absolute/path/to/pi PI_MCP_MODULE_PATH=/absolute/path/to/bridge.mjs acp-agent --adapter pi
```

`PI_PATH` / `pi.Options.Executable` 可省略，默认从完整环境 PATH 发现 `pi`。Ally 的可执行文件填写 `pi` 或其绝对路径。`PrefixArgs` 放在固定 RPC 参数之前；`Environment` 是完整子进程环境，nil 继承宿主。不自动安装或登录。

使用自动审批或托管 MCP 时，宿主为 `pi.Options.MCPModulePath` 提供可读取的绝对文件路径。独立命令读取 `PI_MCP_MODULE_PATH`。bridge 由 Pi 官方扩展加载器导入，提供 `createMcpAdapter`、`classifyAction`、`createReviewSubject` 和 `completeSimple`。宿主负责固定 bridge 与 Pi 的兼容版本。

协议能力包括文本/思考流、图片/嵌入上下文、工具调用状态与内容结果、取消、会话 new/load/resume/list/close/delete、真实模型与思考配置。工具结果按原生 content 投影为 ACP 内容块，当前不生成结构化文件差异或 Bash 终端退出码。历史直接使用 Pi 原生 JSONL；load 回放消息，resume 不回放。每个活跃会话拥有独立 Pi 进程和 MCP 快照，恢复先关闭旧进程，支持更新凭据。`agent_settled` 才结束提示；中间 `agent_end` 不结束重试或自动压缩。执行和取消由对应原生会话处理。

技能和提示模板由 Pi 原生加载。文本输入通过 `prompt` RPC 交付，原生返回 `handled` 时按已处理命令结算；上下文压缩通过宿主 Compact 入口调用原生 `compact` RPC。当前 ACP 不发布 slash 命令目录。Pi 的项目资源信任规则仍由 Pi 管理；不要假定未信任工作区会自动加载扩展。

MCP 直接复用 `pi-mcp-adapter@2.32.1` 公开配置工厂，支持 STDIO、Streamable HTTP、SSE。acp-go 只在 Go 模板中保存会话扩展源码，运行时生成权限为 0600 的临时 `extension.ts`，并让 Pi 从宿主的 bridge 路径导入固定版本的上游实现；会话关闭后删除临时文件，不删除宿主模块。不合并或改写用户配置，参数/环境/请求头保持字面量。acp-go 的构建与测试不需要 npm。

`PermissionMode` 提供 `default`、`auto`、`full-access` 三档。默认档使用上游 MCP 审批和官方工具 hook；仅 cwd 与显式附加目录内已存在目标的 `read/grep/find/ls` 免询问，写入和其他操作仍需确认。目录在新建、恢复或分叉接纳时规范化并固定，首次扩展加载继续校验接纳时的原身份；身份无法验证、符号链接越界、根被替换、原生 cwd 改绑或目标尚不存在均不获得免询问读取。尚未核实与原生同解的 `@`、`~`、file URL 等特殊路径形式保留人工确认，不把 MCP readOnlyHint 当成授权。

自动档继续使用当前所选模型和既有风险分类器，并提供冻结根及其当前绑定情况；判断不明或失败时转人工，不保证授权根内操作一定自动放行。完全访问跳过本扩展的工具审批，仍受操作系统和外部服务限制。附加目录通过原生追加系统指令提供路径发现，包括完全访问模式；路径发现与工具审批均不构成独立操作系统沙箱，也不能消除检查后文件系统再次变化的竞争窗口。

宿主需要完整问答时使用 `acpserver.NewWithUserInput`。Go 进程内提供受管 AskUserQuestion，Pi 第一轮就能调用，问题始终等待真实用户。普通 select/input/editor 转为 ACP form；只有确认和 MCP 审批选项进入 RequestPermission。取消、跳过与回答分别处理。没有 pi-acp 或额外问答进程，不提供独立操作系统沙箱。

新增真实进程回归见 `scripts/unified-integration/`：三档权限、所选模型参与审查、失败转人工、外部 MCP 副作用、问答回填与一分钟以上等待。原始会话/文件/终端/三传输回归保留在 `scripts/pi-integration/`。
