# Kimi Adapter

启动官方 `kimi acp`；CLI 入口为 `acp-agent --adapter kimi`。`kimi.Config.KimiPath` 或 CLI 环境变量 `KIMI_PATH` 指定已安装程序。安装和登录由用户按照 [Kimi Code](https://github.com/MoonshotAI/kimi-code) 或 [Python Kimi CLI](https://github.com/MoonshotAI/kimi-cli) 文档完成。

模型、MCP 和会话能力由 initialize / session 配置决定，兼容新 configOptions 与旧 models/set_model。TypeScript Kimi 0.41.0 已用隔离 HOME 验证 initialize、无认证错误和本地假 provider 的模型目录；真实账号模型调用未验证。

Python 实现通过官方 `KIMI_SHARE_DIR` 使用私有配置副本，模型/思考选择不会保存到用户真实 config.toml。会话与刷新凭据保存在适配器拥有的缓存目录；关闭只删除临时配置。`StateDirectory` 可显式指定持久根。新进程会复制更新的用户凭据，同时保留受管目录中更新的刷新结果。仅迁移已知凭据、配置和设备标识，不复制整个用户目录或历史会话。

`PermissionMode` 提供 default/auto/full-access。Python 副本设置 default_yolo，自动档在原生执行前门禁里使用当前所选模型进行无工具审查；复用 pi-auto-approval 的提示和决策协议，失败或不明确时继续转人工，审查临时历史不混入用户会话。TypeScript 使用原生 default/auto/yolo 会话模式，调用方仍需发送标准 set_mode。TypeScript 的 `KIMI_CODE_HOME` 不受 Python 隔离变量影响。

隔离会话目录使用符号链接；macOS 已测试，Windows 需要允许创建符号链接，尚未实机验证。启动目录应与所恢复会话的 cwd 一致。来源见 [UPSTREAM](../nativeacp/UPSTREAM.md)。


使用 `acpserver.NewWithUserInput` 接入统一问答。TypeScript 原生 elicitation 的顶层 sessionId 被保留到宿主元数据；Python ACP 的原生 AskUserQuestion 会丢弃答案，因此使用进程内 MCP 提供同名工具。执行前确认该目录已经被 CLI 发现，失败会阻止执行。受管进程允许长时间 MCP 问答等待，不修改用户真实配置；取消仍立即传播。Python 的 MCP 工具访问可能先触发原生工具审批，实际问题仍走独立表单，任何权限档位都不能代答。

真实 Python CLI 三档权限、模型选择、风险审查、问答和外部 MCP 拒绝副作用，已由 `scripts/unified-integration/` 的 loopback 模型覆盖；另有超过一分钟的真实问答等待回归。线上供应商认证和真实模型质量不属于这个离线测试的结论。

## Usage

Python Kimi currently discards `StatusUpdate` in ACP. The Go adapter reads only
the appended portion of the managed session's official `wire.jsonl` after each
prompt. It maps Kosong's noncached input, output, cache read/write, and context
counts to standard ACP usage. Missing/invalid data stays unknown. It does not
modify the CLI, patch Python modules, or scan unrelated sessions. This mapping
follows MoonshotAI/kimi-cli commit `86f136422a0aae6b217ea49e7ea1d2e8a1defcd2`
(`metadata.py`, `wire/file.py`, `wire/types.py`, and Kosong `TokenUsage`).
TypeScript Kimi is a separate implementation: its native ACP context update is
forwarded, but its ACP currently omits per-turn usage. The Python file mapping
is never applied to TypeScript sessions.
