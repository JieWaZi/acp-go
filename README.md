# acp-go

`acp-go` 将本机 CLI 接入 [Agent Client Protocol（ACP）](https://agentclientprotocol.com/)。Go 宿主直接使用厂商客户端。独立命令 `acp-agent` 把同一个客户端投影到标准输入输出上的 ACP 连接。

| 客户端 | Go 包 | 独立命令选择 | 原生协议 |
| --- | --- | --- | --- |
| Codex | [codex](pkg/codex) | 默认或 `--adapter codex` | `codex app-server` |
| Claude Code | [claude](pkg/claude) | `--adapter claude` | `claude` stream-json |
| Cursor | [cursor](pkg/cursor) | `--adapter cursor` | `cursor-agent acp` |
| Pi | [pi](pkg/pi) | `--adapter pi` | `pi --mode rpc` |
| WorkBuddy | [workbuddy](pkg/workbuddy) | `--adapter workbuddy` | `codebuddy --acp` 或 `cbc --acp` |
| Qoder / Qoder CN | [qoder](pkg/qoder) | 由 Go 宿主选择 | 原生 ACP |
| Kimi | [standardacp](pkg/standardacp) | `--adapter kimi` | `kimi acp` |
| Gemini CLI | [standardacp](pkg/standardacp) | `--adapter gemini` | `gemini --acp` |
| Grok Build | [standardacp](pkg/standardacp) | `--adapter grok` | `grok --no-auto-update agent stdio` |
| OpenCode | [standardacp](pkg/standardacp) | `--adapter opencode` | `opencode acp` |

[agentclient](pkg/agentclient) 提供原生客户端合同、进程所有权和 ACP 投影。[acpclient](pkg/acpclient) 管理标准 ACP 连接、会话、回调和执行，也供宿主接入自定义 ACP。[acpmeta](pkg/acpmeta) 提供协商后的元数据合同，[acpserver](pkg/acpserver) 提供独立命令的注册和服务边界。

## 源码构建

Go 版本以 [go.mod](go.mod) 为准。当前源码使用本仓库 [SDK 子模块](sdk) 中的会话接纳及响应回执接口。本地开发工作区需同时纳入两个模块。

```sh
acp_source="$(pwd)"
acp_workspace="$(mktemp -d)"
go -C "$acp_workspace" work init "$acp_source" "$acp_source/sdk"
export GOWORK="$acp_workspace/go.work"
go build -o ./acp-agent ./cmd/acp-agent
```

独立模块发布需先发布含这些接口的 SDK，再更新 acp-go 的 SDK 依赖。当前发布版固定依赖尚缺这些接口。引用 acp-go 的本地宿主也需将 acp-go 和 SDK 纳入宿主工作区。

## 在 ACP 客户端中使用

将 `command` 设为 `acp-agent` 的绝对路径，在 `args` 中选择适配器，并将 `cwd` 设为项目目录。Gemini 与 WorkBuddy 将原生进程绑定到启动目录，ACP 会话使用同一目录。客户端应分别读取协议 stdout 与诊断 stderr。

```json
{
  "command": "/absolute/path/to/acp-agent",
  "args": ["--adapter", "codex"],
  "cwd": "/absolute/path/to/project"
}
```

CLI 由用户安装并按厂商流程登录。可选执行路径使用 `CODEX_PATH`、`CLAUDE_CODE_EXECUTABLE`、`CURSOR_PATH`、`PI_PATH`、`WORKBUDDY_PATH`、`KIMI_PATH`、`GEMINI_PATH`、`GROK_PATH` 或 `OPENCODE_PATH`。Pi 的 MCP bridge 通过 `PI_MCP_MODULE_PATH` 配置。

客户端按初始化能力发送 `mcpServers`、Elicitation 和会话操作。原生会话、模型、权限与用量以各 CLI 的握手和回执为准。

## 作为 Go 包使用

使用各包的 `NewClient(ctx, Options, HostCallbacks)`。构造冻结 CLI、前置参数和完整环境，`Initialize` 执行启动和能力协商。`Environment: nil` 继承当前环境。宿主提供消息观察和交互回调，结束使用时关闭客户端。

直接 Go 调用通过 `ReservePrompt` 获取执行函数，并读取独立的输入接纳、原生终态、用量和交付错误。协议服务使用 `NewACPAgent` 构造共享客户端，再交给 `acpserver`。

```go
agent, err := agentclient.NewACPAgent(func(host agentclient.HostCallbacks) (agentclient.Client, error) {
    return codex.NewClient(ctx, codex.Options{
        Executable: os.Getenv("CODEX_PATH"),
    }, host)
})
if err != nil {
    return err
}
server, err := acpserver.NewWithUserInput(agent, os.Stdin, os.Stdout)
if err != nil {
    return err
}
return server.Serve(ctx)
```

`Serve` 在断线或取消时关闭客户端。仅在真实握手确认的范围内使用原生 fork、恢复、模型设置和权限模式。连续对话只发送本轮新增输入。

## 开发与验证

适配器和服务入口的常规测试使用 fake CLI 与冻结报文。按修改范围运行对应包。

```sh
go test ./pkg/agentclient ./pkg/acpclient ./cmd/acp-agent
go test ./pkg/codex ./pkg/claude ./pkg/cursor
go test ./pkg/pi ./pkg/workbuddy ./pkg/qoder ./pkg/standardacp
go run ./tools/protocolgen --check
```

Codex 生成协议用 `go generate ./pkg/codex/protocol` 刷新。原生 ACP 证据见 [测试矩阵](docs/NATIVE_ACP_TEST_MATRIX.md)。真实 CLI、账号和模型运行单独验证。
