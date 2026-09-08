# 原生 ACP 来源与兼容基线

核对日期：2026-09-08。优先复用已经存在的完整协议实现，仅在适配边界处理配置与交互语义。

| 来源 | 基线 | 复用方式 |
| --- | --- | --- |
| [coder/acp-go-sdk](https://github.com/coder/acp-go-sdk) | go.mod v0.13.5，Apache-2.0 | 标准 Go 协议、传输与调度 |
| [MoonshotAI/kimi-code](https://github.com/MoonshotAI/kimi-code) | 源码 fa87d040c3086cdceb3bee24762451cf47af0336，MIT；实测 npm 0.41.0 | 原生 ACP、配置类别及模式 |
| [MoonshotAI/kimi-cli](https://github.com/MoonshotAI/kimi-cli) | 86f136422a0aae6b217ea49e7ea1d2e8a1defcd2，Apache-2.0 | 原生 ACP、旧 models、官方配置隔离入口 |
| [Cursor ACP](https://cursor.com/docs/cli/acp) | 官方文档与 2026-06-22 更新日志；CLI 非开源依赖 | 原生 ACP 及官方 Cursor 扩展 |
| [openclaw/acpx](https://github.com/openclaw/acpx) | d8eaddc2ef7e0e147f2ec09cec0acd83a763ef2d，MIT | 对照命令与兼容语义 |
| [Pi 组合](../pi/UPSTREAM.md) | pi-acp 0.0.33 + Pi 0.84.1 + pi-mcp-adapter 2.32.1，MIT | 直接调用发布包，不重写 RPC/MCP |

上游仓库已 clone 并对照实现。Kimi / Cursor 能力由用户安装版本握手确认；官方文档不等于本机真实进程测试。Pi 源码 HEAD 与发布包不同，已分别验证 HEAD 的 95 项测试和锁定发布包真实进程链路。没有把社区项目名称当作所有能力成熟的证明。

升级需要重新核对模型、交互选项、MCP 参数、隐式恢复、取消与进程退出，再执行测试矩阵。
