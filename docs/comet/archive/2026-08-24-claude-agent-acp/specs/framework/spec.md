# Framework complete target specification

## SDK boundary

- `acp-agent` 直接使用 acp-go-sdk v0.13.5 的 `AgentSideConnection`、Agent 类型、可选 Loader 和 ExtensionMethodHandler，不实现自己的 ACP JSON-RPC connection、dispatch 或 cancel 层。
- 协议服务 stdout 只写 ACP 帧，启动、版本和运行时诊断只写 stderr。

## Core and composition

- Core 只定义 Adapter 元数据、工厂、注册/选择和通用生命周期边界，不包含 Codex 或 Claude 的 session/turn/process/tool/permission 类型。
- 默认选择名保持 `codex`；无参数与显式 `--adapter codex` 等价，显式 `--adapter claude` 构造 Claude，未知名称在建立 ACP connection 前 fail fast。
- composition root 同时显式注册 Codex 与 Claude 工厂；未选中的工厂不得执行或探测对应 CLI。
- composition root 将 stderr logger 和 `CLAUDE_CODE_EXECUTABLE` 值显式传给 Claude；Claude 包自行完成 PATH fallback。
- 可选能力继续使用消费方小接口；构造函数返回具体类型，禁止全局注册、service locator、DI 容器和覆盖所有 Adapter 的 Runtime 大接口。

## Connection and cleanup

- Claude 可复用 acpserver 已有 connection binder 与 adapter closer 注入/清理时序；除非实现发现经测试证明的 SDK 能力缺口，不修改 server 协议职责。
- ACP 连接结束或进程 context 取消时，acpserver 在独立有界 context 中调用 Claude Close；Close 释放所有 Session 进程和等待者并保持幂等。

## Acceptance mapping

- A1：默认/显式 Adapter 选择及 Codex 无回归。
- A2：Claude capability 只声明已实现功能。
- A16：connection/Adapter 关闭释放所有资源。
- A18：组合根、回归和工程验证。
