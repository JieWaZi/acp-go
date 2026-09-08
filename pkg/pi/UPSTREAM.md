# Pi 复用来源

核对日期：2026-09-08。用户确认将 pi-acp 的适配能力移植到我们维护的 acp-go，用 Go 直连 Pi；pi-acp 仅为源码参考，无运行依赖。

| 项目 | 固定来源 | 用途及许可 |
| --- | --- | --- |
| [svkozak/pi-acp](https://github.com/svkozak/pi-acp/tree/d1cffc047ab37a096ee70ca39cfc1de463db8d12) | d1cffc047ab37a096ee70ca39cfc1de463db8d12 | MIT；Go 协议适配的行为与实现参考，许可保留于 UPSTREAM-LICENSE |
| [Pi](https://github.com/earendil-works/pi) | @earendil-works/pi-coding-agent 0.84.1 / 53fa77ccd8a279eb87e92294ef3687b03ff80112 | MIT；直接调用 CLI 官方 RPC，模型执行、历史及资源加载由 Pi 负责 |
| [pi-auto-approval](https://github.com/Europa2061/pi-auto-approval/tree/33de98cb79d8a9a23300e9826504e5b69c6e1b52) | 0.1.0 / 33de98cb79d8a9a23300e9826504e5b69c6e1b52 | Apache-2.0；复用 classifyAction、提示与当前模型凭据解析，许可包含在 MCP-LICENSES.txt |
| [pi-mcp-adapter](https://github.com/nicobailon/pi-mcp-adapter) | 2.32.1 / 10a45367e033a32026987a75d6f401e37340c86f | MIT；内置其公开 createMcpAdapter 工厂，保留 MCP 协议、连接及审批实现 |

Go 移植对照：

| 参考文件 | 本库实现 |
| --- | --- |
| src/pi-rpc/process.ts | rpc.go、process_unix.go、process_other.go：进程、JSON 行、请求关联、取消和回收 |
| src/acp/agent.ts、pi-sessions.ts | agent.go、sessions.go、configuration.go、prompt.go：会话、模型、思考、历史、分页、命令 |
| src/acp/session.ts、translate/* | events.go、tool_output.go：流式消息、工具、差异、终端、审批及终态 |

改进：独立会话进程替代共享进程切换，避免 MCP 凭据串会话；使用原生历史 ID，避免维护第二份私有映射；排队请求支持 Context 取消；input/editor 使用 ACP form elicitation（宿主不支持则取消）。提供 default/auto/full-access，不把思考模式当权限；普通 select/input/editor 使用表单，MCP 的三种审批选项保留正确的授权作用域。

`extension.ts` 组合开源 MCP 工厂、风险分类模块和官方工具 hook，不重写 MCP 客户端。只引入分类模块，不载入可以关闭保护的全局配置/命令入口；不采用自动允许工作区写入或外部 readOnlyHint 的快捷路径。分类失败或否决时转人工审批。`tools/pi-extension/entry.ts` 是固定源码组合入口。构建工具位于 `tools/pi-extension`，npm 依赖由 package-lock.json 固定，esbuild 生成 `mcp.generated.ts` 及 `MCP-LICENSES.txt`，第三方许可证随 Go 包交付。Pi 自身的包保持外部引用，由 Pi 官方 extension loader 解析。不要手改生成文件。没有使用 AGPL 的 pi-golang 实现。

原 pi-acp 0.0.33 的进程组合已被替换；其先前测试结果不替代 Go 移植测试。升级需重跑 Go/race 与真实 Pi 集成，覆盖模型、历史、审批副作用、三种 MCP 传输、特殊字符、文件差异、终端、输入、模板和内置命令。
