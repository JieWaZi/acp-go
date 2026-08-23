# Framework child complete target specification

## SDK boundary

- `acp-agent` 必须直接使用 acp-go-sdk v0.13.5 的 `AgentSideConnection`、Agent 类型和可选 Loader，不实现自己的 ACP JSON-RPC 连接层。
- 协议服务的 stdout 只写 ACP 帧，启动和诊断错误写 stderr。

## Core and composition

- Core 只定义 Adapter 元数据、工厂、注册/选择和通用生命周期边界，不包含任何 Codex thread/turn/sandbox/approval 类型。
- 默认选择名是 `codex`；显式 `--adapter codex` 等价，未知名称 fail fast。
- composition root 显式构造依赖；禁止 `init()` 注册、全局 service locator 和 DI 容器。
- 可选能力使用消费方小接口；构造函数返回具体类型，避免提前建立超大 Runtime 接口。

## Readability and verification

- 手写 Go 类型、字段和函数使用清晰中文注释；关键注册冲突、选择和协议启动失败解释原因。
- 表驱动测试覆盖注册、重复注册、默认/显式/未知选择和协议边界。
