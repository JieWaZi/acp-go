# Framework complete target specification

## Purpose

系统是一个 stdio ACP Agent 进程。V1 提供 Agent-neutral 的 ACP Core 和首个 Codex Adapter；未来 Adapter 可以拥有不同的原生状态与可选能力，但必须通过同一核心生命周期接入。

## Architecture style

- 采用轻量 Ports-and-Adapters：Core contracts 定义 ACP 生命周期和必要的 Adapter 端口，Codex package 实现首个适配器，protocol package 隔离生成类型，`cmd/acp-agent` 负责显式组装。
- 不建立独立 use-case、repository、domain entity、aggregate 或框架式 application/service 层；没有当前职责的层和接口不得创建。
- Codex Adapter 内部文件和职责尽量保持与固定 codex-acp 模块对应，Core 只抽取已经由 ACP 生命周期证明通用的边界。

## ACP boundary

- 使用固定版本 `github.com/coder/acp-go-sdk` 的 Agent-side connection 实现 ACP JSON-RPC 连接、请求分发、取消通知和扩展方法。
- Agent 启动后接收 ACP Client 的 initialize、session 和 prompt/control 请求，并将 connection 能力注入所选 Adapter。
- Core 直接组合 SDK 的类型、Agent-side connection 和扩展接口，不复制或重新包装 SDK 已经提供的 JSON-RPC、dispatch、cancel、extension 协议栈；只有 Adapter 选择与生命周期编排属于本项目职责。

## Composition and selection

- `go.mod` 在本地开发阶段使用模块名 `acp-go`；本变更不绑定尚未确定的远端仓库路径，也不执行 push、PR 或发布。未来确定发布地址后以独立变更迁移 module path。
- 进程 composition root 显式构造 logger、ACP connection、Adapter registry/selector 和 Codex Adapter；不使用隐式 `init()` 注册、全局 service locator 或 DI 容器。
- Registry 保存具体 Adapter 工厂及其元数据；选择结果在建立 session 前确定。
- V1 交付单一 `acp-agent`：未提供 `--adapter` 时选择 Codex，`--adapter codex` 显式选择同一实现；未来新增 Adapter 时默认值仍为 Codex，其他 Adapter 必须显式选择。
- 未知或不可用 Adapter 在 ACP 协议开始前返回可诊断启动错误，不回退到任意实现；V1 不为 Codex 另建独立二进制。
- 构造参数较少时使用普通显式构造；只有可选配置确实会独立演进时使用可返回校验错误的 functional options，不引入 builder 或框架化容器。

## Adapter contract

- 核心合同只包含 initialize/session/prompt/cancel 所需的最小生命周期。
- Load/Resume、Steering、Authentication、Configuration 等能力通过消费方定义的小接口按需检测，不进入一个超大 Runtime 接口。
- 构造函数接收接口并返回具体类型；只有真实第二实现或测试替身需要时才定义接口。
- Core 只保存通用 ACP session 标识、Adapter 归属、context/cancel 与日志关联信息。
- Codex ThreadID、TurnID、Sandbox、Approval、Token Usage、MCP 和 SteeringQueue 状态不得进入 Core 的公共模型。
- 必要扩展点仅包含 Adapter 注册/选择、可选能力以及外部进程/协议边界；没有第二个真实需求或测试边界时不得新增抽象。

## Lifecycle

- 每个外部活动都由 context 约束；连接关闭时取消全部 session 活动并关闭 Adapter 资源。
- Adapter/session 注册、替换和关闭必须是并发安全的，不允许关闭后的异步 open 覆盖新 generation。
- 资源上限和清理责任必须显式；不得启动无所有者 goroutine 或无界队列。

## Code readability

- 所有手写 Go 类型、结构体字段和函数具有清晰中文注释；exported 声明同时符合 Go doc comment 的标识符开头习惯。
- 函数内部只在关键状态转换、并发顺序、异常兜底和复杂映射处解释原因及不变量，不为自明赋值堆叠噪声注释。
- Protocol Generator 输出不适用中文注释强制要求；生成文件只保留标准生成标记与上游 schema 原始文档，并由 freshness 检查保证无人手工修改。
- 错误分支优先返回，主路径保持扁平；预期的协议、进程、输入和环境失败返回 error，不 panic。

## Acceptance mapping

- A1：ACP SDK boundary 与 initialize。
- A2：Core/Adapter 中立性、注册选择和可选能力。
- A16：架构与上游可追溯性。
- A18：中文注释和关键逻辑可读性。
