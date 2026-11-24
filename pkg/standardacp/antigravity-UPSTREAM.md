# Antigravity CLI ACP 兼容证据

最低版本为 `agy 1.5.0`。CLI 的 `agy acp` 公布 ACP v1 和 `Antigravity ACP Server` 身份。ACP Registry 的独立 `antigravity-acp` 二进制有自己的协商和发行版本。[CLI 发布记录](https://github.com/google-antigravity/antigravity-cli/blob/main/CHANGELOG.md) · [CLI ACP 协议](https://antigravity.google/docs/ide/extensions/acp/)

2026-11-24 使用 `agy 1.5.0` 和 `1.5.15`、临时 profile 和只访问本机的 Gemini 模拟端点。1.5.0 使用核对 SHA-256 的官方 macOS arm64 发布包。启动 `agy acp --process-per-session` 公布 HTTP MCP、图片、附加目录和会话恢复能力。省略该参数时，1.5.15 握手省略 HTTP MCP。已测两个连续 prompt 及退出后以原 UUID 恢复。模型和 `thought_level` 的设置回执反映了所选值，随后请求发往选定模型的端点。冷恢复返回账号默认模型和思考强度，宿主需要重新应用聊天的已选参数。CLI 可接受同一 profile 中跨目录的原生 UUID，宿主须维持原绑定和授权目录。隔离目录探测通过模型设置回执分别读取各个模型的思考档位，原生版本实测中有低、高档组合，也有中档和额外高档。[1.5.0 官方下载](https://github.com/google-antigravity/antigravity-cli/releases/expanded_assets/1.5.0)

两个实测版本均以 `cancelled` 结束已取消请求，并继续完成下一轮。重启后的首个模型请求包含原会话前两轮输入。两个版本均要求使用 `--gemini-dir` 指定非默认数据根，才能读取该根下的 `antigravity-cli/settings.json`。测试中仅设置 `GEMINI_HOME` 时，原生会话创建返回认证要求。

两个实测版本对找不到的 UUID 返回 `session/resume` 内部错误。这个错误可以结合工作目录下的会话列表确认缺失。1.5.15 的 `session/load` 对同一 UUID 返回明确的缺失错误。1.5.0 的 `session/load` 会应答同一个不存在的 UUID，需检查同一会话的历史回放才可发布加载结果。

模拟端点返回 100 输入、20 缓存读、5 回答和 7 思考。ACP 两轮各返回 `inputTokens: 80`、`cachedReadTokens: 20`、`cachedWriteTokens: 0`、`outputTokens: 12`、`thoughtTokens: 7` 和 `totalTokens: 112`。首次请求伴随一个标题生成调用，该调用未计入主回答回执。每轮 Token 以 prompt 响应为准。上下文窗口使用量独立展示。[T3 Code 的 Antigravity 设置](https://github.com/pingdotgg/t3code/blob/cd128e7774020aac59b30adffd537fc3dcf5563e/apps/server/src/orchestration-v2/Adapters/AntigravityAdapterV2.ts) · [回执字段口径](https://github.com/pingdotgg/t3code/blob/cd128e7774020aac59b30adffd537fc3dcf5563e/packages/provider-acp/src/server/adapter.ts)

原生轨迹位于 `antigravity-cli/brain/<id>/.system_generated/logs/transcript_full.jsonl`。实测完成的 `PLANNER_RESPONSE` 带 `step_index`、模型和 Token 字段。两轮可在同一秒写入，回执和 JSONL 均缺少可关联的调用标识。缺少 ACP 用量时保留未知。[CC Switch 的原生轨迹导入](https://github.com/farion1231/cc-switch/blob/721ca8e08c5233f41e9a79cc367d581ac1ec4bcd/src-tauri/src/services/session_usage_agy.rs#L273-L322)

CLI 文档提供交互式 `/fork`。`agy 1.5.15` 的 ACP 握手和会话命令列表均未公布分叉。直接请求 `session/fork` 返回 `-32601`。`--conversation <id> -p /fork --output-format json` 返回错误及空子会话 ID。程序化原生分叉待上游接口支持与验证。[CLI 交互式分叉](https://antigravity.google/docs/cli/conversations/) · [公开 SDK 会话 API](https://github.com/google-antigravity/antigravity-sdk-python/tree/main/google/antigravity/conversation)
