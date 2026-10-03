# Gemini CLI Adapter（当前基础接入）

`gemini.NewAgent(ctx, gemini.Config{Logger: logger})` 使用已安装且支持 `--acp` 的 Gemini CLI；命令入口为 `acp-agent --adapter gemini`。`GeminiPath` 或 `GEMINI_PATH` 指定路径。旧 CLI 的 `--experimental-acp` 不自动重试，固定基线为 0.62.0。

目前公开真实 legacy `availableModels/currentModelId` 到标准 `model` 配置，通过原生 `session/set_model` 接受非空准确模型 ID，包括宿主真正发现的账号自定义模型。未知模型不会被塞入 catalog 或获配臆造的 thinking 菜单。思考内容流保留 `agent_thought_chunk`；本轮 `_meta.quota.token_count` 映射标准 usage，不把累计调用 token 当上下文占用率。

原生 Load 回放历史；本包 Resume 使用官方 Load 并屏蔽历史通知。固定 CLI 没有原生 close 与即时 steering；close 保留 MethodNotFound，steering 不声明可用。MCP 与普通工具/模式取实际原生能力。原生 ACP 忽略额外目录；如需启动目录扩展，可使用官方 `--include-directories` 作为 PrefixArgs，由 CLI 自身加载指令/技能。CLI 自身上下文与历史不由 Go 复制。

账号 A、B 各用独立 `GEMINI_CLI_HOME` 根（其下才是 `.gemini`），配合原生 Google 协议的 API key 和 endpoint 环境：

```go
environment := nativeacp.WithEnvironment(os.Environ(), "GEMINI_CLI_HOME", "/managed/account-a/gemini")
environment = nativeacp.WithEnvironment(environment, "GEMINI_API_KEY", accountKey)
agent, err := gemini.NewAgent(ctx, gemini.Config{
    Logger: logger, Environment: environment, WorkingDirectory: "/managed/project-a",
})
```

私有临时 user settings 保留原始配置，账号所配 home 保存原生持久状态。未显式给 GEMINI_CLI_HOME 时，只读取用户 `.gemini` 配置/凭据，把状态与凭据刷新放在适配器自己的稳定缓存目录；`StateDirectory` 可指定该缓存。清理临时设置不会删除原生持久会话。完整 `Environment`（nil 继承）保留调用者账号/API key/profile，不修改真实 settings。

**待完成的能力**：统一 thinking setter 和 auto/full-access 权限仍在实现。官方 customAliases/customOverrides 经 core resolver 和 Google SDK 证明可正确序列化参数，但真实 CLI 会在模型路由或工具 continuation 后丢失 alias override，因此当前公开入口不以该机制宣称强度生效。temporary SYSTEM_SETTINGS_PATH 还要求 root 所有权，不可用于普通用户覆盖；使用私有 user profile 不绕过该检查。接下来的兼容层需证明 native session 的完整多回合生命周期与实际 provider 请求配置，才能公开可选档位。

来源、候选配置验证和升级见 [UPSTREAM.md](UPSTREAM.md)，实际覆盖见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。
