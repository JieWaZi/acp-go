# Kimi Adapter

启动官方 `kimi acp`；CLI 入口为 `acp-agent --adapter kimi`。`kimi.Config.KimiPath` 或 CLI 环境变量 `KIMI_PATH` 指定已安装程序。安装和登录由用户按照 [Kimi Code](https://github.com/MoonshotAI/kimi-code) 或 [Python Kimi CLI](https://github.com/MoonshotAI/kimi-cli) 文档完成。

模型、MCP 和会话能力由 initialize / session 配置决定，兼容新 configOptions 与旧 models/set_model。TypeScript Kimi 0.41.0 已用隔离 HOME 验证 initialize、无认证错误和本地假 provider 的模型目录；真实账号模型调用未验证。

Python 实现通过官方 `KIMI_SHARE_DIR` 使用私有配置副本，模型/思考选择不会保存到用户真实 config.toml。会话与刷新凭据保存在适配器拥有的缓存目录；关闭只删除临时配置。`StateDirectory` 可显式指定持久根。新进程会复制更新的用户凭据，同时保留受管目录中更新的刷新结果。仅迁移已知凭据、配置和设备标识，不复制整个用户目录或历史会话。

`PermissionMode=default/full-access` 为 Python 副本设置 default_yolo。TypeScript 使用原生 default/auto/yolo 会话模式，调用方仍需发送标准 set_mode；旧实现不支持 auto 或只读时宿主必须拒绝这些等级。TypeScript 的 `KIMI_CODE_HOME` 不受 Python 隔离变量影响。

隔离会话目录使用符号链接；macOS 已测试，Windows 需要允许创建符号链接，尚未实机验证。启动目录应与所恢复会话的 cwd 一致。来源见 [UPSTREAM](../nativeacp/UPSTREAM.md)。
