# Grok Build Adapter

`grok.NewAgent(ctx, grok.Config{Logger: logger})` 启动官方已安装的 `grok agent stdio`；命令入口 `acp-agent --adapter grok`。`GrokPath` 或 `GROK_PATH` 指定路径，`PrefixArgs`、完整 `Environment`（nil 继承）与 `WorkingDirectory` 控制进程。

模型与真实 reasoning_effort 值来自原生模型服务。仅统一配置 ID 为 `model`/`reasoning`，保留全部原生值、默认标记及不支持 effort 时的省略。思考流独立于配置。会话、MCP、工具、用量、附加目录、指令和技能以当前原生声明及事件为准，不虚构其他 CLI 的能力。

`PermissionMode` 支持 default（原生 ask）、auto（原生 LLM/启发式风险分类器）和 full-access（原生 yolo）。创建、Load 与 Resume 使用官方 `_meta.autoMode`/`yoloMode`，保留其他 metadata。原生 requirements/managed 规则和 auto kill switch 仍有权收紧权限；请求 auto 不承诺违反管理员规则。auto 不等于全权限。

`_session/steering` 转发至官方 `_x.ai/interject`，支持文本与原生图片内容。本包返回 `outcome: "queued"`，只确认进入 native safe-point 队列。它不等于模型已消费的 `injected`，也不保证空闲时开始新回合；宿主可显式继续标准 Prompt。握手 `_meta.steering.mode` 为 `queued`。其他扩展原样经 ACP SDK 转发。

xAI 第一方账号/项目示例（`XAI_API_KEY` 仅放对应 xAI 第一方凭据）：

```go
environment := nativeacp.WithEnvironment(os.Environ(), "GROK_HOME", "/managed/account-a/grok")
environment = nativeacp.WithEnvironment(environment, "XAI_API_KEY", xaiFirstPartyKey)
agent, err := grok.NewAgent(ctx, grok.Config{
    Logger: logger, Environment: environment, WorkingDirectory: "/managed/project-a",
    PermissionMode: "auto",
})
```

账号 B 使用另一 `GROK_HOME`。官方 `auth.json`、`config.toml` 与 sessions 都位于该根。第三方受管端点在此根的 `[model.<id>]` 配置实际 `model`、匹配供应方的 `base_url` 及模型专属 `api_key`（或专属 `env_key`）；若继承目录已有 `api_base_url`，也须核对 API-key 路由的实际地址。不要把第三方账号密钥放入全局 `XAI_API_KEY`，使其用于原生默认 xAI 模型列表地址。

固定源码的 `api_backend` 按 snake_case 接受 `chat_completions`、`responses`、`messages`。`auth_scheme` 是独立的原生模型/采样字段，取值 `bearer`、`x_api_key`，分别决定 Authorization Bearer 或 `x-api-key`；选择 `messages` 协议本身不等于切换认证方案。固定源码的 `[model.<id>]` override 含 `api_backend`，但没有 `auth_scheme` 字段，因此本文不提供未经验证的 TOML 认证覆盖示例。这些是源码支持范围；尚未用真实 Grok 可执行文件、第三方端点或付费模型验证端到端协议兼容。

来源和升级见 [UPSTREAM.md](UPSTREAM.md)，测试边界见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。

新建、Load、Resume 原生响应同时返回旧版 `models` 与 `configOptions`。本包将旧版逐模型 `_meta.supportsReasoningEffort` / `reasoningEfforts` 投影到匹配的实际 model select 选项的 `acp-go/model-config-options`；输入 `id`（可与规范 `value` 不同）、label、description 和源 default 标记保持原样。明确支持且菜单缺失/空数组时，精确采用固定上游 fallback：minimal、low、medium、high、xhigh，全部 default=false；只在 fallback 中排除 none/max，明确提供的菜单可保留它们。明确 false 标记为已知空目录；标记缺失、畸形或未知菜单保持未知，不根据模型名称推断。

当前模型始终以实际 `configOptions` 为准，包括菜单、未列出的 currentValue，以及 reasoning 项的省略。模型 setter 回执和异步 configOptionUpdate 都重新应用此规则，不将旧会话当前值覆盖到新菜单。缺少有效逐模型 metadata 时，宿主仍须实际切换目标模型、读取原生 setter 回执，并按需恢复原选择。
