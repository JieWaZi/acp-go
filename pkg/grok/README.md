# Grok Build Adapter

`grok.NewAgent(ctx, grok.Config{Logger: logger})` 启动官方已安装的 `grok agent stdio`；命令入口 `acp-agent --adapter grok`。`GrokPath` 或 `GROK_PATH` 指定路径，`PrefixArgs`、完整 `Environment`（nil 继承）与 `WorkingDirectory` 控制进程。

模型与真实 reasoning_effort 值来自原生模型服务。仅统一配置 ID 为 `model`/`reasoning`，保留全部原生值、默认标记及不支持 effort 时的省略。思考流独立于配置。会话、MCP、工具、用量、附加目录、指令和技能以当前原生声明及事件为准，不虚构其他 CLI 的能力。

`PermissionMode` 支持 default（原生 ask）、auto（原生 LLM/启发式风险分类器）和 full-access（原生 yolo）。创建、Load 与 Resume 使用官方 `_meta.autoMode`/`yoloMode`，保留其他 metadata。原生 requirements/managed 规则和 auto kill switch 仍有权收紧权限；请求 auto 不承诺违反管理员规则。auto 不等于全权限。

`_session/steering` 转发至官方 `_x.ai/interject`，支持文本与原生图片内容。本包返回 `outcome: "queued"`，只确认进入 native safe-point 队列。它不等于模型已消费的 `injected`，也不保证空闲时开始新回合；宿主可显式继续标准 Prompt。握手 `_meta.steering.mode` 为 `queued`。其他扩展原样经 ACP SDK 转发。

独立账号/项目示例：

```go
environment := nativeacp.WithEnvironment(os.Environ(), "GROK_HOME", "/managed/account-a/grok")
environment = nativeacp.WithEnvironment(environment, "XAI_API_KEY", accountKey)
agent, err := grok.NewAgent(ctx, grok.Config{
    Logger: logger, Environment: environment, WorkingDirectory: "/managed/project-a",
    PermissionMode: "auto",
})
```

账号 B 使用另一 `GROK_HOME`。官方 `auth.json`、`config.toml` 与 sessions 都位于该根。自定义模型使用该受管根中的 `[model.<id>]` 与实际 `model`/`base_url`/`api_key`/`env_key`；支持其官方 OpenAI-compatible chat 协议，不宣称 Responses 或 Anthropic 协议等价。

来源和升级见 [UPSTREAM.md](UPSTREAM.md)，测试边界见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。

原生目录只返回当前模型的 effort 列表；不把它复制给其他模型。宿主需实际切换目标模型并读取 setter 回执来发现对应档位，然后按需恢复原选择。固定上游没有每个模型的配置 metadata，因此默认模型目录中的其他模型不宣称拥有已知强度。
