# OpenCode、Gemini CLI、Grok Build 与 Ally 接入设计

用户已经批准开发三个适配器，并要求与 Codex、Claude 同样提供模型、thinking、项目账号配置，以及完成后更新文档和 Ally。

## 架构

以官方 CLI 的原生 ACP 为执行核心，Go SDK 承担双向代理、进程生命周期、客户端回调与公共 API。OpenCode 启动 `opencode acp`，Gemini CLI 启动 `gemini --acp`，Grok Build 启动 `grok agent stdio`。公共包分别为 `pkg/opencode`、`pkg/gemini`、`pkg/grok`；共享实现复用最新主线已有的 `pkg/nativeacp`，禁止重复建立 transport。三个公共包提供与现有包一致的 Config、NewAgent、Agent、Close、SetAgentConnection。工作基线为 48b2b12，包含已有 Kimi、Cursor、Pi 适配器；其默认行为必须保留，新生命周期语义由显式配置启用。

上游基线：OpenCode v1.18.34；Gemini CLI v0.62.0；Grok Build 提交 2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8。acpx v0.19.4 仅作为互操作实现参考。各包 UPSTREAM.md 保存固定提交、入口、能力映射和差异。

Gemini 官方 ACP 缺少 thinking 配置接口，且别名配置在工具续轮中会丢失强度。兼容层为每个公共会话拥有独立原生进程，启动时设置对应 canonical 模型的固定配置。闲置时切换模型或 thinking，通过新进程初始化并恢复真实历史，成功后交换；忙碌时明确拒绝配置切换。公共会话标识和回调保持稳定，关闭会话释放其真实进程且保留历史。原生恢复会在同一分钟内覆盖同名记录，兼容层必须先在私有状态目录保护逐字节真实记录，处理重复候选、最新历史及失败回滚，禁止合成对话冒充恢复。

## 验收约束

- 使用真实 ACP 请求和事件承载会话、内容、工具、审批、文件、终端、MCP、模型和思考配置；禁止靠伪造 capability 或硬编码模型列表冒充支持。
- 兼容旧 models/modes 接口与 configOptions，并保存模型相关 effort/thinking 的真实可选值；模型切换后刷新实际配置。
- thinking 流与 thinking 档位是两个独立能力，分别验证。没有可发现档位时明确返回能力边界，不能从模型名字推断。
- 原生能力不足时，优先使用官方扩展或最小、可追溯的兼容层。不能把会中 steering 偷换成取消重开；不能从累计 token 伪造上下文占用。
- Config.Environment 是完整环境替换，nil 继承父环境，PrefixArgs 放在原生启动参数前；复制调用者切片。账号、配置、会话目录必须能够按实例隔离。不得写用户真实 CLI 目录或输出凭据。
- 会话中的 prompt 保持 FIFO；取消、关闭、进程退出能够解除等待。默认权限下审批和用户问题通过当前外层 ACP 连接回传，不能无条件自动允许。
- 新增三种运行时提供 default、auto、full-access 权限。auto 使用真实原生自动审批或同一官方 CLI 的无工具评审；兼容层仅在当前会话、模型和完整工具证据仍一致且评审明确允许时授予 allow_once，并发出审计元数据。拒绝、错误、缺失证据或无法安全执行评审时回人工。Gemini auto_edit 不代表自动风险审批。
- agentInfo.version 表示适配器版本；实际 CLI 版本通过现有 acpmeta 暴露。
- stdout 只写 ACP 协议，日志及子进程诊断写 stderr。错误保留 JSON-RPC 的分类和元数据。
- 不更改 Codex、Claude 的行为，不包含主工作区用户已有 .gitignore 改动。
- 所有新增手写 Go 声明、字段和重要逻辑遵循仓库中文注释要求。
- Ally 使用同一能力和账号路径，更新运行时发现、账号配置、模型/thinking 选择、MCP 兼容性、指令/技能挂载与执行恢复，不能只添加三个菜单文字。

## 测试与交付

默认测试采用真实 SDK 和假 CLI 子进程，不依赖账号或模型调用；覆盖双向请求、事件、错误、环境隔离、队列、取消、关闭与崩溃。官方 CLI 冒烟检查仅验证可用的版本/初始化，无账号时不声称完成真实模型验证。新增 README、上游追溯与测试矩阵准确说明兼容性。

Ally 在独立工作区改动，遵循其 AGENTS.md、CONTEXT.md 和后端/前端/UI 文档；无须浏览器操作。完成后运行两个项目要求的检查，保留可审阅的本地分支。
