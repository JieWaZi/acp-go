# Pi Adapter

复用 [svkozak/pi-acp](https://github.com/svkozak/pi-acp) 的 ACP / Pi RPC 桥和 [pi-mcp-adapter](https://github.com/nicobailon/pi-mcp-adapter) 的 MCP 客户端。薄扩展只组合公开配置工厂及 Pi 官方工具审批 hook。

在同一 npm 目录安装已验证版本（Node.js >= 22.19），并按 Pi 官方文档配置模型提供方：

```sh
npm install --prefix "$HOME/.local/share/ally-pi" @earendil-works/pi-coding-agent@0.84.1 pi-acp@0.0.33 pi-mcp-adapter@2.32.1
PI_ACP_PATH="$HOME/.local/share/ally-pi/node_modules/.bin/pi-acp" acp-agent --adapter pi
```

库和 Ally 不自动执行安装或登录。在 Ally 的 Pi CLI 路径中选择这个 `pi-acp`，不能选择 `pi`。依赖默认从同一安装解析；分开安装时用 `pi.Config.PiPath` / `PI_ACP_PI_COMMAND` 指定 Pi，用 `MCPModulePath` / `PI_MCP_ADAPTER_PATH` 指向 MCP 包的 `index.ts`。完整进程 PATH 仍需包含 Node。

支持 STDIO、Streamable HTTP、SSE MCP。每次 new/load 生成私有扩展快照；不会合并或改写用户 MCP 配置。会话切换、模型和思考等级切换恢复对应快照，load 可更新凭据。关闭进程后删除临时配置。Pi 上游只保持一个活跃进程，因此同一 Adapter 的会话操作串行；并行任务应使用不同 Agent。

`PermissionMode` 支持 `default` 与 `full-access`。默认通过现成 MCP 插件审批 MCP 工具，其他非只读工具用官方 `tool_call` hook 逐次确认；`pi-acp` 将 select/confirm 转成标准 ACP 审批。宿主必须按实际选项 ID 回传，不能仅凭 allow_once kind 自动放行（上游的 Deny 也使用该 kind）。完全访问关闭本扩展的审批门。它不是文件系统或网络沙箱，不提供 auto/read-only 权限等级。上游扩展 input/editor 尚未桥接，不能在业务侧声称支持任意 Pi 交互。

模型和推理使用实际 configOptions；Pi 原生 modes 是思考等级。来源与许可证见 [UPSTREAM](UPSTREAM.md)，真实进程集成命令见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。
