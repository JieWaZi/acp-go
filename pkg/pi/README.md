# Pi Adapter

调用链是 **ACP 客户端 → acp-go → `pi --mode rpc`**。acp-go 参照 MIT 开源项目 [svkozak/pi-acp](https://github.com/svkozak/pi-acp) 用 Go 移植协议适配；不安装、不发现、不启动 `pi-acp`。

用户只需安装 Pi（已验证 0.84.1，Node.js >=22.19），在 Pi 原生终端配置模型提供方和登录：

```sh
npm install -g @earendil-works/pi-coding-agent@0.84.1
pi
PI_PATH=/absolute/path/to/pi acp-agent --adapter pi
```

`PI_PATH` / `pi.Config.PiPath` 可省略，默认从完整环境 PATH 发现 `pi`。Ally 的可执行文件填写 `pi` 或其绝对路径。`PrefixArgs` 放在固定 RPC 参数之前；`Environment` 是完整子进程环境，nil 继承宿主。不自动安装或登录。

协议能力包括文本/思考流、图片/嵌入上下文、工具状态、文件差异、Bash 终端输出/退出码、取消、会话 new/load/resume/list/close/delete、真实模型与思考配置。历史直接使用 Pi 原生 JSONL；load 回放消息，resume 不回放。每个活跃会话拥有独立 Pi 进程和 MCP 快照，恢复先关闭旧进程，支持更新凭据。`agent_settled` 才结束提示；中间 `agent_end` 不结束重试或自动压缩。排队请求可取消。

对照 pi-acp 提供 `/compact`、`/autocompact`、`/export`、`/session`、`/name`、`/steering`、`/follow-up`、`/changelog`，技能和提示模板交给 Pi 原生加载。与参考版本一致，命令目录不发布依赖交互终端的扩展 slash 命令。Pi 的项目资源信任规则仍由 Pi 管理；不要假定未信任工作区会自动加载扩展。

MCP 直接复用 `pi-mcp-adapter@2.32.1` 公开配置工厂，支持 STDIO、Streamable HTTP、SSE。该开源扩展及其依赖已生成并嵌入 Go 二进制，在同一个 Pi 进程内加载，无需用户安装额外 MCP 包。临时配置权限 0600，关闭后删除；不合并或改写用户配置，参数/环境/请求头保持字面量。`MCPModulePath` 仅保留显式开发覆盖。

`PermissionMode` 支持 `default` 与 `full-access`：默认使用成熟 MCP 插件审批及 Pi 官方 `tool_call` hook 审批其他非只读工具。Go 将 select/confirm 转成 ACP 权限请求，按实际选项 ID 回传；input/editor 通过支持 form elicitation 的 ACP 客户端收集输入，否则取消。超时、拒绝和提示取消不会自动批准。完全访问关闭本扩展审批门；不提供独立沙箱或 auto/read-only 权限等级。

维护者重新生成嵌入资源：

```sh
npm ci --prefix tools/pi-extension
npm run --prefix tools/pi-extension build
```

普通 Go 构建使用已提交资源，不需要 npm。来源、移植范围与许可证见 [UPSTREAM](UPSTREAM.md)，验证命令见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。
