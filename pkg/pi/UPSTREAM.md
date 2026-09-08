# Pi 复用来源

核对日期：2026-09-08。以下包共同安装并经过真实进程测试，模型服务与 MCP 服务使用本地测试实现，不使用真实账号。

| 包 | 已发布版本 / gitHead | 许可证与用途 |
| --- | --- | --- |
| [pi-acp](https://github.com/svkozak/pi-acp) | 0.0.33 / 1bfcb394088ed879db8fd936b570bb626017f878 | MIT；完整复用 ACP、Pi RPC、扩展 select/confirm 桥 |
| [Pi](https://github.com/earendil-works/pi) | @earendil-works/pi-coding-agent 0.84.1 / 53fa77ccd8a279eb87e92294ef3687b03ff80112 | MIT；CLI、会话、模型调用和官方 tool_call hook |
| [pi-mcp-adapter](https://github.com/nicobailon/pi-mcp-adapter) | 2.32.1 / 10a45367e033a32026987a75d6f401e37340c86f | MIT；createMcpAdapter({config})、全部 MCP 传输与工具审批 |

`extension.ts` 是本库的配置组合代码，不包含上述项目的协议实现。审批 hook 对照 Pi 官方扩展示例与 [beyond5959/acp-adapter](https://github.com/beyond5959/acp-adapter) 的 MIT Pi gate；没有移植该项目的消息类型或 ACP 后端。上游许可证随 npm 依赖保留。

发布包与仓库 HEAD 不同；测试不得以 HEAD 测试通过代替发布包验证。升级时重跑本仓库 scripts/pi-integration：隐式会话恢复、MCP 参数字面量、工具拒绝和加载失败都影响正确性。
