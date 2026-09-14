# Cursor Adapter

Ally 只对接 acp-go。`cursor.NewAgent` 和 `acp-agent --adapter cursor` 始终在 Go 内管理官方 Cursor 交互终端，并使用官方 `cursor-agent acp` 获取认证、模型目录与配置回执。不存在绕过受管交互语义的第二种公开模式，也无需安装另一个 ACP 桥、Python 或 tmux。官方 Cursor 自身使用其附带的 Node；用量 Hook 的内置小脚本只传输白名单字段，不承载审批或业务语义。

`CursorPath`（命令入口的 `CURSOR_PATH`）指定安装路径，默认发现 `cursor-agent`，不存在时尝试 `agent`。同一连接固定解析后的 CLI 版本，避免后台自动更新使控制与执行进程版本不同。调用者须关闭 Agent 以回收进程和临时配置。

## 执行与统一语义

- 连续轮次复用同一个交互进程；模型或工作模式改变时重启终端并恢复原生聊天身份。原生模型和参数回执写入会话独立配置，并通过官方参数式 `--model` 覆盖历史模型。目录暂时退回旧格式时，只在发送消息前以相同参数最多重启三次；不猜测别名或降级模型。
- 三档权限为 `default`（原生逐工具门禁）、`auto`（官方 `--auto-review`）、`full-access`（官方 `--force`）。`agent/plan/ask` 是独立工作模式。恢复时通过官方 `/run-everything`、`/auto-review` 本地命令同步历史权限，并只读核对实际元数据，防止从全权限切回默认后仍自动执行。宿主收到的审批携带当前界面与检查点唯一对应的原始工具身份和参数；只有回执到达且当前检查点仍一致时才发送批准或拒绝键。
- 原生 AskQuestion 使用共享 `userinput` / ACP Elicitation 语义，支持单选、多选及自由文本。工具参数、结果和拒绝事实进入同一 ACP 工具事件流，供宿主审计。
- MCP 通过会话私有插件注入 STDIO 和 Streamable HTTP，包括环境和请求头。不会修改用户 MCP 配置。旧 SSE 当前不声明支持。
- 原生 SQLite 日志只读增量投影。冷恢复使用 SQLite 一致快照把已提交 WAL 内容交给官方 ACP 读取；跨连接文件锁防止同一身份被同时恢复或写入。Resume 不重放历史，Load 使用官方历史回放。
- 官方 stop Hook 映射本轮输入、输出、缓存读取与写入；输入总量包含缓存，映射时拆出非缓存输入，避免重复计数。按生成身份去重，缺失用量返回未知而非零。

## Hook 与数据边界

Cursor 2026.09.08 的普通交互路径尚未触发插件范围 stop Hook，因此在工作目录 `.cursor/hooks.json` 临时添加本会话的独立 Hook。文件锁与精确条目回收保留原有 Hook 和用户同期修改；正常取消、失败和关闭会清理条目。Ally 工作目录为聊天自己的受管目录。调用者若把真实项目作为 cwd，需要接受这项临时文件写入；进程被强制杀死时可能留下条目，当前尚无崩溃后自动清理保证。

持久聊天默认在用户缓存目录 `acp-go/cursor` 下，可用 `StateDirectory` 指定。临时 CLI 配置权限为 0600，Hook 只保留会话、生成、状态和用量字段，不保存邮件或凭据。用户配置、MCP 配置与实际执行库不被恢复快照覆盖。

## 已验证与边界

2026-09-09，macOS、官方 Cursor `2026.09.08-6caf4ff`：真实拒绝不产生文件、批准产生文件、连续轮次、进程退出后记住前轮内容、中文自由回答、多选、真实 HTTP MCP 审批与一次实际调用、逐轮 Token 回填均已通过。库测试还覆盖 PTY/SQLite/ACP 往返、取消及进程回收、WAL 快照、所有权、Hook 并发清理和数据竞争。

2026-09-12 的回归新增批量工具审批身份核对、MCP 命名空间匹配、同前缀连续粘贴、目录缺失限次重启，以及真实 CLI 跨进程 `full-access → default → auto → default` 权限状态核对。`ALLY_CURSOR_REAL_PROBE=1 go test ./pkg/cursor -run TestCursorRealPermissionTransitions -count=1 -v` 只操作本地权限命令，不调用模型；普通测试默认跳过此入口。

当前交互提示只支持文本；二进制图片/音频附件没有实现，能力声明不承诺支持。上下文精确快照仅来自官方 preCompact Hook，不能提供每轮连续上下文计量，也不拿累计计费 Token 冒充上下文。Windows PTY 尚未实现。SQLite 私有结构与终端交互存在上游版本敏感性；不匹配时返回明确失败，不伪造审批、用量或完成。尚不能把所有 Claude/Codex 的专有能力视为等价支持。

## 参考与许可

- [Omnigent](https://github.com/omnigent-ai/omnigent/tree/45601bae8d98a25bf4d193187481e0a79d49dde5/omnigent/harnesses/cursor_native)：原生日志、pending 检查点、审批与问答设计；Go 改写。Apache-2.0，保留 [LICENSE](licenses/omnigent-LICENSE) 和相关 [NOTICE](licenses/omnigent-NOTICE)。
- [cursor-local-acp](https://github.com/fat-huhu/cursor-local-acp/tree/b932be54e244bfdba4b2a5f66135bdd9e08ca70e)：交互桥与用量来源对照。MIT，见 [LICENSE](licenses/cursor-local-acp-LICENSE)。
- VT 模拟、PTY 和 SQLite 分别直接使用 Charm x/vt、creack/pty、ncruces/go-sqlite3 的直接连接接口（不注册全局 SQL 驱动），不自行实现终端或数据库。
