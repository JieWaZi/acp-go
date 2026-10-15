# OpenCode Adapter

`opencode.NewAgent(ctx, opencode.Config{Logger: logger})` 启动已安装的 `opencode acp`；命令入口为 `acp-agent --adapter opencode`。`OpenCodePath` 或 `OPENCODE_PATH` 指定路径。`PrefixArgs` 放在 ACP 参数之前，`Environment` 是完整环境（nil 继承），`WorkingDirectory` 是进程 cwd。关闭 Agent 回收唯一原生进程。

`PermissionMode` 支持 default（沿用原生策略）、full-access（官方 `OPENCODE_PERMISSION` allow）及 auto（原生 ask，加同 CLI/同当前模型的风险审查）。auto 的 reviewer 在私有工作目录和全部 XDG/HOME 中执行 `run --format json`，仅保留选中 provider 与凭据，关闭所有模型工具、MCP、用户插件、自定义工具和外部技能。只有完整的当前回合证据、审查 allow、宿主审计成功且会话代/模型未变化时才选择 `allow_once`；deny、错误或证据不足都回退宿主审批。审计使用现有 `acp-go/permission-review` metadata，不输出凭据。

安全审查边界：JSONC 注释、额外 `.opencode`/父目录/CONFIG_DIR provider 配置、文件替换、远程 well-known 配置、managed policy 或非官方 SDK 模块均保守转人工。只允许固定上游依赖的官方 SDK 模块；禁用内置 auth 插件导致 OAuth 无法完成时也转人工。当前只读检查 console active-org 数据库配置；有组织覆盖、无法读取或未知数据库时转宿主审批，不复制组织 token。独立账号环境仍按下方路径隔离。

原生 `configOptions` 提供真实 provider/model、variants 和模式。本包仅将 `thought_level` 选项 ID 统一为 `reasoning`，写入时恢复原生 `effort` ID。目录、可选值和模型切换后的新目录均以 CLI 回执为准。思考文本是独立 `agent_thought_chunk` 流，不代表已设置思考强度。

OpenCode 原生支持 new/load/resume/close/list/fork；能力仍取当前实际握手。Load 回放原生历史，Resume 使用原生无回放方法。MCP、附加目录、原生指令与技能遵循 OpenCode 自身实现，Go 不复制上下文。统一问答可通过 `acpserver.NewWithUserInput` 注入现有 MCP 工具。当前统一即时 steering 不支持：并发 native prompt 等待整轮完成，不能把它当作立即插话确认。

账号 A 和 B、不同项目各使用独立环境，例如：

```go
environment := nativeacp.WithEnvironment(os.Environ(), "XDG_DATA_HOME", "/managed/account-a/data")
environment = nativeacp.WithEnvironment(environment, "XDG_CONFIG_HOME", "/managed/account-a/config")
environment = nativeacp.WithEnvironment(environment, "XDG_CACHE_HOME", "/managed/account-a/cache")
environment = nativeacp.WithEnvironment(environment, "XDG_STATE_HOME", "/managed/account-a/state")
agent, err := opencode.NewAgent(ctx, opencode.Config{
    Logger: logger, Environment: environment, WorkingDirectory: "/managed/project-a",
})
```

实际凭据在 `$XDG_DATA_HOME/opencode/auth.json`。只覆盖 `OPENCODE_CONFIG_DIR` 不隔离账号。项目或账号 provider 设置使用官方 `opencode.json`；`Environment` 中 API key、代理/profile 与原生 provider 环境变量完整传递。协议与 provider 的端点设置不可互换，沿用官方 provider npm 支持的协议。

固定版本、来源路径、限制及升级流程见 [UPSTREAM.md](UPSTREAM.md)；无账号 fake CLI/真实 SDK 的验证边界见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。

原生目录只返回当前模型的 effort 列表；不把它复制给其他模型。宿主需实际切换目标模型并读取 setter 回执来发现对应档位，然后按需恢复原选择。固定上游没有每个模型的配置 metadata，因此默认模型目录中的其他模型不宣称拥有已知强度。
