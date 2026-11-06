# Cursor

`cursor.NewClient` 使用官方 `cursor-agent acp`，由 [acpclient](../acpclient) 独占执行、实时文本、审批、取消和会话生命周期。Cursor 客户端处理原生握手、模型配置、账号及配额。`Options.UsageHook` 为已核对版本启用独立用量采集，NeuxBot 在 Cursor 构造入口选用此能力。

用量钩子仅作用于本客户端拥有的 Node 子进程。它在模块内存加载时核对精确路径、完整摘要和唯一接入点，从原生 `turnEnded` 采集四类计数；不修改 CLI 安装文件、项目配置、原生提示、权限、文本流或终态。未知版本、摘要不匹配和采集失败沿用普通 ACP，Token 保持未知。

每个预留输入都有独立 nonce。单轮结束后，现有 `CapturePromptJournal` 用 nonce、原生会话、request ID 和 user message ID 对照 ACP 会话库权威 root，再补充调用用量。计数重复不累加，冲突或缺失保持未知。原生输入包含缓存，缓存读写齐全时才计算普通输入。尚未证明覆盖全部请求的计数展示为已知小计，不把所选模型冒充实际消费模型。

构造时固定实际安装路径，避免 CLI 自动更新让版本探测与执行混用安装。原生配置、数据根和凭据来自调用方冻结的环境。私有采集目录随客户端生命周期清理，原生进程未回收时保留原所有者以便重试。

`Options.Executable` 为空时查找 `cursor-agent`，也识别指向该 CLI 的官方 `agent` 别名。权限档支持原生默认和 `full-access`；原生 ACP 不支持的自动审查明确拒绝。

定向验证使用 `go test ./pkg/cursor`。协议来源见 [原生 ACP 兼容基线](../standardacp/NATIVEACP-UPSTREAM.md)，参考实现许可见 [omnigent-LICENSE](omnigent-LICENSE)、[omnigent-NOTICE](omnigent-NOTICE) 和 [cursor-local-acp-LICENSE](cursor-local-acp-LICENSE)。
