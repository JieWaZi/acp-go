# Native ACP adapters implementation plan

> 执行方式：subagent-driven-development；每个任务测试优先，实现后单独审查规范与质量。

**Goal:** 为 acp-go 新增 OpenCode、Gemini CLI、Grok Build 的生产可用适配器，并在 Ally 同步接入运行时、账号与模型/thinking 能力。

**Architecture:** 官方原生 ACP CLI + internal/nativeacp 双向进程代理 + 三个轻量公共适配器，供应商差异由可追溯兼容层处理。

**Tech Stack:** Go 1.25.8、coder/acp-go-sdk v0.13.5、官方 CLI；Ally Go 后端和 Svelte/TypeScript 桌面端。

**Spec:** docs/superpowers/specs/2026-10-03-native-acp-adapters-design.md

## Global Constraints

- 使用真实 ACP 请求和事件；禁止伪造 capability、模型列表或 thinking 档位。
- Config.Environment 完整替换；nil 继承；PrefixArgs 在原生参数前；复制切片；项目账号与状态按实例隔离。
- prompt FIFO；取消、关闭、崩溃必须解除等待；审批和用户问题回传外层客户端，不自动允许。
- stdout 只写协议；保留 JSON-RPC 错误；adapter version 与实际 CLI version 分离。
- 不修改 Codex、Claude 既有行为或用户已有 .gitignore；新增 Go 声明和字段写中文注释。
- 默认测试真实 SDK + 假 CLI，无凭据、无真实模型/网络依赖。
- 会中 steering 不得实现为取消重开；累计 token 不得冒充上下文占用。未支持能力明确暴露，不能声称已达到完整对齐。
- Ally 要同步账号、模型/thinking、MCP、技能/指令和恢复执行，不能仅改菜单。

### Task 1: Native ACP process bridge

**Files:** 新增 internal/nativeacp/{agent,process,client,session,extensions}.go，按职责添加 *_test.go；允许必要的同目录小文件。

1. [ ] 先写行为测试并观察失败：真实 ACP 外层连接→代理→假 CLI 进程；假进程由测试二进制 helper 启动，stdout 不污染协议。
2. [ ] Config 描述可执行文件、默认文件名、PrefixArgs、Environment、原生命令参数、Logger、适配器身份；NewAgent 只解析选中的 CLI。明确路径失败不回退 PATH；复制切片；独立探测 --version 并按 acpmeta 注入。
3. [ ] 实现 acp.Agent 与 AgentLoader、SetAgentConnection、Close。透传已存在的标准会话/配置方法以及 SDK 支持的可选接口；不要把方法缺失改成假成功。
4. [ ] 子进程到外层客户端的文件、terminal、request_permission、user_input、elicitation、session_update 双向转发；保留 payload、_meta、错误 code/data。按 SDK 可选接口转发。
5. [ ] 原生子进程按需启动、可重复 Initialize，初始化能力从原生响应得来；外层绑定未完成时回调显式失败。关闭幂等、进程退出唤醒待处理请求；日志无敏感数据。
6. [ ] 每个 session prompt FIFO，可并发不同 session；Cancel 不中断别的会话。排队请求支持 context 取消，CloseSession/Close 不挂住；新建/load/resume 建立会话记录，closed session 不复用。
7. [ ] 可转发未知 ACP 扩展，给三个适配器供应商兼容留清晰、窄接口。避免因扩展处理器无法区分 request/notification 引入循环。
8. [ ] 测试并验证完整环境替换、prefix 参数、CLI 版本与 build 版本、所有回调、扩展错误、FIFO、取消、关闭、进程崩溃和可选方法；运行 go test ./... 和桥接 race 测试一次。
9. [ ] 自审、提交、写详细报告（含 RED/GREEN 与命令输出）。

### Task 2: Public adapters, source-backed normalization and documentation

**Files:** pkg/opencode、pkg/gemini、pkg/grok 的 agent.go、doc.go、README.md、UPSTREAM.md、行为测试；必要的 internal/nativeacp 供应商兼容代码；cmd/acp-agent/main.go 及其测试；根 README.md、docs/NATIVE_ACP_TEST_MATRIX.md。

1. [ ] 阅读 Task 1 的公共接口，先编写三个适配器行为测试并观察失败。
2. [ ] 三个包公开 Config（Logger、供应商 Path、PrefixArgs、Environment）、NewAgent、Agent，命令分别 opencode acp、gemini --acp、grok agent stdio；注册 adapter ID opencode、gemini、grok。
3. [ ] 从固定上游代码查验、实现模型/configOptions/legacy models 与真实 thinking/effort 的发现和更新。必要时做源代码支持的兼容映射；thinking 内容转发必须单独测试。
4. [ ] 查验并处理原生 session resume/close/history、steering、MCP、usage、额外目录和指令/技能差异。官方扩展可用于补齐，不能无依据宣称。任何无法对齐的关键能力必须在报告中具体列出并请求控制器决策，不能悄悄改验收标准。
5. [ ] 查验各 CLI 实际账号隔离路径，给出三种独立实例/项目配置例子；验证 API key/profile env 完整传递；OpenCode 除配置目录还要隔离真正 auth.json 数据目录。
6. [ ] 固定上游版本解析到 commit SHA，UPSTREAM.md 记录代码路径、关键能力与限制、acpx 参考来源及升级步骤；源码可临时放忽略目录，不能 vendoring 大量无关实现。
7. [ ] 更新 README 包导入和 CLI 列表、能力矩阵、账号配置和模型/thinking 说明；测试矩阵区分已测 fake CLI、原生无账号冒烟与真实模型未测。
8. [ ] 运行 gofmt、go vet ./...、go test ./...，必要的 race 检查，构建 acp-agent；自审、提交并报告。

### Task 3: Ally integration

**Files:** /Users/ryan/Desktop/aix/ally 中运行时 catalog、ACP factory、Runtime Account、project execution config、MCP compatibility、instruction/skill delivery、生成 API 与桌面运行时选择/配置相关现有文件，以及 README/CONTEXT/运行时文档。

1. [ ] 创建适当隔离工作区（若原工作区无改动也保留独立分支）；完整阅读 Ally AGENTS.md、CONTEXT.md、docs/backend-engineering-conventions-cn.md、docs/frontend-engineering-conventions-cn.md、docs/ui-design-system-cn.md 后再改动。
2. [ ] 找出 Codex/Claude 的现有运行时发现、账号、安全路径、模型/thinking 目录、项目默认配置、执行与恢复链路。测试优先扩展三种运行时。
3. [ ] 同步使用 Task 2 公共包，接入真实二进制发现/版本/健康状态、账号与项目配置、环境隔离、模型和 thinking 选择/事件、会话恢复、权限/问答、MCP 原生传输兼容性、指令及技能交付。所有能力来自桥接和上游，不引入假默认。
4. [ ] 前端使用现有组件设计与数据接口，更新固定 runtime 联合类型/标签/表单/显示。若 API 变化通过现有生成器更新 OpenAPI/TS，不手工篡改生成文件。
5. [ ] 禁止为依赖提交本机绝对 replace 路径；使用项目现有本地依赖机制，必要时通过临时 Go workspace 验证两个 worktree 并说明发布依赖要求。
6. [ ] 更新 README、运行时支持与账号设置文档，并准确写能力边界。禁止浏览器/UI 自动化（用户未授权）。
7. [ ] 后端 gofmt、go vet、go test，前端 pnpm --dir desktop check、pnpm api:check，跨层 pnpm check；记录任何环境阻碍及原始输出。自审、提交并报告。

### Task 4: Final integration verification and review

1. [ ] 针对模型变化、thinking、项目账号环境、重启恢复和 MCP 兼容执行跨层验证，审查缺失能力，不能把尚未满足的 parity 标记完成。
2. [ ] 整体分支审核和一个集中修复回合，执行覆盖最终变更的必需检查。
3. [ ] 保留两个可审阅本地分支，报告测试、实际模型验证范围、能力边界与发布依赖。不得未经授权 push/publish/merge。
