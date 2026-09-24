# Pi 复用来源

核对日期：2026-09-08。用户确认将 pi-acp 的适配能力移植到我们维护的 acp-go，用 Go 直连 Pi；pi-acp 仅为源码参考，无运行依赖。

| 项目 | 固定来源 | 用途及许可 |
| --- | --- | --- |
| [svkozak/pi-acp](https://github.com/svkozak/pi-acp/tree/d1cffc047ab37a096ee70ca39cfc1de463db8d12) | d1cffc047ab37a096ee70ca39cfc1de463db8d12 | MIT；Go 协议适配的行为与实现参考，许可保留于 UPSTREAM-LICENSE |
| [Pi](https://github.com/earendil-works/pi) | @earendil-works/pi-coding-agent 0.84.1 / 53fa77ccd8a279eb87e92294ef3687b03ff80112 | MIT；直接调用 CLI 官方 RPC，模型执行、历史及资源加载由 Pi 负责 |
| [pi-auto-approval](https://github.com/Europa2061/pi-auto-approval/tree/33de98cb79d8a9a23300e9826504e5b69c6e1b52) | 0.1.0 / 33de98cb79d8a9a23300e9826504e5b69c6e1b52 | Apache-2.0；Ally 的 bridge 复用 classifyAction、提示与当前模型凭据解析，许可随 Ally 安装包交付 |
| [pi-mcp-adapter](https://github.com/nicobailon/pi-mcp-adapter) | 2.32.1 / 10a45367e033a32026987a75d6f401e37340c86f | MIT；Ally 的 bridge 导出其公开 createMcpAdapter 工厂，保留 MCP 协议、连接及审批实现 |

Go 移植对照：

| 参考文件 | 本库实现 |
| --- | --- |
| src/pi-rpc/process.ts | rpc.go、process_unix.go、process_other.go：进程、JSON 行、请求关联、取消和回收 |
| src/acp/agent.ts、pi-sessions.ts | agent.go、sessions.go、configuration.go、prompt.go：会话、模型、思考、历史、分页、命令 |
| src/acp/session.ts、translate/* | events.go、tool_output.go：流式消息、工具、差异、终端、审批及终态 |

改进：独立会话进程替代共享进程切换，避免 MCP 凭据串会话；使用原生历史 ID，避免维护第二份私有映射；排队请求支持 Context 取消；input/editor 使用 ACP form elicitation（宿主不支持则取消）。提供 default/auto/full-access，不把思考模式当权限；普通 select/input/editor 使用表单，MCP 的三种审批选项保留正确的授权作用域。

Go 字符串模板在运行时生成 `extension.ts`，组合宿主 bridge 的 MCP 工厂、风险分类模块和官方工具 hook，不重写 MCP 客户端。只引入分类模块，不载入可以关闭保护的全局配置/命令入口；不采用自动允许工作区写入或外部 readOnlyHint 的快捷路径。分类失败或否决时转人工审批。Ally 在 `desktop/pi-bridge` 固定上游版本、生成单文件 bridge 与第三方许可；acp-go 不提交或内嵌 TS/JS 构建产物。Pi 自身的包保持外部引用，由 Pi 官方 extension loader 解析。没有使用 AGPL 的 pi-golang 实现。

原 pi-acp 0.0.33 的进程组合已被替换；其先前测试结果不替代 Go 移植测试。升级需重跑 Go/race 与真实 Pi 集成，覆盖模型、历史、审批副作用、三种 MCP 传输、特殊字符、文件差异、终端、输入、模板和内置命令。

## 用量补齐（2026-09-08）

pi-acp 当前参考实现没有完成 Token 投影，因此统计另外对照 Pi 0.84.1 的 `AssistantMessage.usage` 与官方 `ExtensionContext.getContextUsage()`：Go 累计当前 Prompt 内的输入、输出、缓存读取/创建；`extension.ts` 仅转发官方上下文快照。上下文是 Pi 的估计值，压缩后未知时不编造数字。Pi 没有独立 reasoning token 字段，费用不进入 Ally 现有 Token 契约。版本直接探测 `pi --version`，不显示适配器版本。

## 项目目录与分叉验收（2026-09-23）

核对 Pi 0.85.1 的 `dist/core/tools/path-utils.js` 与 `dist/utils/paths.js`：原生工具支持绝对路径，不提供附加目录沙箱开关。Adapter 对 `additionalDirectories` 校验绝对路径及目录存在性，创建、加载、恢复和分叉采用相同规则；项目入口和项目指令仍由宿主提供，工具调用继续经过既有 default/auto/full-access 审批，不新增免审批路径。此前直接拒绝该参数会阻断 Ally 所有带项目的 Pi 会话。
