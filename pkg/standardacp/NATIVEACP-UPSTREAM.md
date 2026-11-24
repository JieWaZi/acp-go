# 原生 ACP 来源与兼容基线

核对日期：2026-09-08。优先复用已经存在的完整协议实现，仅在适配边界处理配置与交互语义。

| 来源 | 基线 | 复用方式 |
| --- | --- | --- |
| [coder/acp-go-sdk](https://github.com/coder/acp-go-sdk) | go.mod v0.13.5，Apache-2.0 | 标准 Go 协议、传输与调度 |
| [MoonshotAI/kimi-code](https://github.com/MoonshotAI/kimi-code) | 源码 fa87d040c3086cdceb3bee24762451cf47af0336，MIT；实测 npm 0.41.0 | 原生 ACP、配置类别及模式 |
| [MoonshotAI/kimi-cli](https://github.com/MoonshotAI/kimi-cli) | 86f136422a0aae6b217ea49e7ea1d2e8a1defcd2，Apache-2.0 | 原生 ACP、旧 models、官方配置隔离入口 |
| [Cursor ACP](https://cursor.com/docs/cli/acp) | 官方文档与 2026-06-22 更新日志；CLI 非开源依赖 | 原生 ACP 及官方 Cursor 扩展 |
| [openclaw/acpx](https://github.com/openclaw/acpx) | d8eaddc2ef7e0e147f2ec09cec0acd83a763ef2d，MIT | 对照命令与兼容语义 |
| [Pi 组合](../pi/UPSTREAM.md) | pi-acp d1cffc04 行为参考 + Pi 0.84.1 + 宿主提供 pi-mcp-adapter 2.32.1，MIT | 在 Go 中移植 ACP/RPC 适配，MCP 复用公开工厂 |

上游仓库已 clone 并对照实现。Kimi / Cursor 能力由用户安装版本握手确认；官方文档不等于本机真实进程测试。Pi 源码 HEAD 与发布包不同，已分别验证 HEAD 的 95 项测试和锁定发布包真实进程链路。没有把社区项目名称当作所有能力成熟的证明。

升级需要重新核对模型、交互选项、MCP 参数、隐式恢复、取消与进程退出，再执行测试矩阵。

2026-10-15 新增 [WorkBuddy / CodeBuddy Code](../workbuddy/UPSTREAM.md)：固定本机 2.151.0 协议证据，复用标准 `codebuddy/cbc --acp`。公开 question 在厂商包内桥接；标准面不生产私有准入或凭据材料。

2026-11-24 核对 Hermes Agent `0.17.0` 本机安装（源码 `10043c6d0cd942487f7ef94231e22d91e1734a20`）。临时 profile 的 `hermes acp` 公布 `default`、`accept_edits`、`dont_ask`。`session/set_mode` 返回空对象确认，同进程 `session/load` 回报设定后的模式。该安装在重开进程后为同一会话回报 `default`，适配器因此在每次打开会话时按所选模式核对。产品 `accept-edits` 与 `dont-ask` 分别映射到原生 `accept_edits` 与 `dont_ask`，两者仍由 Hermes 对敏感路径发起确认。默认策略在原生会话公布模式时选择 `default`，会话省略模式菜单时沿用 CLI 的当前策略。 [本机安装对应的适配源码](https://github.com/NousResearch/hermes-agent/blob/10043c6d0cd942487f7ef94231e22d91e1734a20/acp_adapter/server.py#L504-L558)

`v0.23.3` Gateway（`caafe5389ae585ef77387a962aee05b1bcf17029`）使用相同的模式 ID。隔离 Gateway 经 Go 适配器验证三种模式、模型目录、两轮聊天、ACP 进程重启后的续聊，并以实际回环模型请求确认 `high` 生效。 [模式定义](https://github.com/NousResearch/hermes-agent/blob/v0.23.3/acp_adapter/modes.py) · [Gateway 适配](https://github.com/NousResearch/hermes-agent/blob/v0.23.3/acp_adapter/gateway_server.py)

本机 `0.17.0` 的会话打开响应提供 `models` 和 `modes`，通用配置 setter 返回空选项目录。Gateway `v0.23.3` 源码公布 `reasoning_effort`，类别为 `thought_level`。`session/set_config_option` 校验原生档位，经 Gateway 会话应用后返回现行选项。 [旧版源码](https://github.com/NousResearch/hermes-agent/blob/10043c6d0cd942487f7ef94231e22d91e1734a20/acp_adapter/server.py#L2023-L2059) · [配置选项](https://github.com/NousResearch/hermes-agent/blob/v0.23.3/acp_adapter/gateway_session_meta.py#L63-L88) · [设置协议](https://github.com/NousResearch/hermes-agent/blob/v0.23.3/acp_adapter/gateway_server.py#L541-L575)

`0.17.0` 使用 ACP 进程独占的累计计数，从相邻回执提取每轮增量。`0.23.3` 的 ACP 回执会混入同一会话中其他客户端的用量。适配器从已选本机 Gateway 的 SQLite 33 只读核对普通文本对应的原生接纳记录。只有完整序号窗口中恰有一条匹配、Gateway 所有者与物理会话一致，且接纳回执与主模型账本增量吻合时，才输出本轮总输入（含缓存）及输出。附件、斜杠命令、相同文本并发和未能核验的会话归属保留未知。`PromptResponse.usage` 保留为原始观察。使用发行源码、本地模拟模型和双 ACP 客户端验证交错提交、ACP 与 Gateway 重启及相同输入的歧义回退。 [Gateway 用量回执](https://github.com/NousResearch/hermes-agent/blob/v0.23.3/acp_adapter/gateway_server.py#L679-L761) · [持久回读](https://github.com/NousResearch/hermes-agent/blob/v0.23.3/persistence/sqlite/authority/_admission_owner.py#L241-L249) · [主模型统计](https://github.com/NousResearch/hermes-agent/blob/v0.23.3/agent/turn_usage.py#L326-L357)
