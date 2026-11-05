# Cursor

`cursor.NewClient` 使用官方 `cursor-agent acp`。标准连接、请求和执行状态由 [acpclient](../acpclient) 管理。厂商客户端处理原生握手、选项、模型目录、交互、账号及配额证据。直接 Go 调用与 `acp-agent --adapter cursor` 共用该客户端。

`Options.Executable` 为空时查找 `cursor-agent`，也识别能解析到该 CLI 的官方 `agent` 别名。完整环境可指定 `CURSOR_API_KEY` 和原生配置根。权限档支持默认和 `full-access`。`auto` 在已核对的原生 ACP 版本中不可用，构造时明确拒绝。

定向验证使用 `go test ./pkg/cursor`。协议来源见 [原生 ACP 兼容基线](../standardacp/NATIVEACP-UPSTREAM.md)，此前使用的参考实现许可见 [licenses](licenses)。
