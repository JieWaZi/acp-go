# acp-go

`acp-go` 将本机 CLI 接入 [Agent Client Protocol（ACP）](https://agentclientprotocol.com/)。Go 宿主直接使用厂商客户端。独立命令 `acp-agent` 把同一个客户端投影到标准输入输出上的 ACP 连接。

| 客户端 | Go 包 | 独立命令选择 | 原生协议 |
| --- | --- | --- | --- |
| Codex | [codex](pkg/codex) | 默认或 `--adapter codex` | `codex app-server` |
| Claude Code | [claude](pkg/claude) | `--adapter claude` | `claude` stream-json |
| Cursor | [cursor](pkg/cursor) | `--adapter cursor` | `cursor-agent acp` |
| Pi | [pi](pkg/pi) | `--adapter pi` | `pi --mode rpc` |
| WorkBuddy | [workbuddy](pkg/workbuddy) | `--adapter workbuddy` | `codebuddy --acp` 或 `cbc --acp` |
| Qoder | [qoder](pkg/qoder) | `--adapter qoder` | `qodercli --acp` |
| Qoder CN | [qoder](pkg/qoder) | `--adapter qoder-cn` | `qoderclicn --acp` |
| Kimi | [standardacp](pkg/standardacp) | `--adapter kimi` | `kimi acp` |
| Gemini CLI | [standardacp](pkg/standardacp) | `--adapter gemini` | `gemini --acp` |
| Grok Build | [standardacp](pkg/standardacp) | `--adapter grok` | `grok --no-auto-update agent stdio` |
| OpenCode | [standardacp](pkg/standardacp) | `--adapter opencode` | `opencode acp` |

[agentclient](pkg/agentclient) 提供原生客户端合同、进程所有权和 ACP 投影。[acpclient](pkg/acpclient) 管理标准 ACP 连接、会话、回调和执行，也供宿主接入自定义 ACP。[acpmeta](pkg/acpmeta) 提供协商后的元数据合同，[acpserver](pkg/acpserver) 提供独立命令的注册和服务边界。[sessionfiles](pkg/sessionfiles) 提供原生会话文件的目录持久化。公共代码统一放在 `pkg`。

## 源码构建

Go 版本与依赖版本以 [go.mod](go.mod) 为准。ACP SDK 通过 Go modules 获取，仓库不内嵌 SDK 源码，不需要本地 `go.work` 或 `replace`。克隆仓库后，在仓库根目录运行：

```sh
GOWORK=off go build -o ./acp-agent ./cmd/acp-agent
```

SDK 从 `coder/acp-go-sdk v0.13.5` 迁入定制补丁并合并官方 main，由独立 fork [JieWaZi/acp-go-sdk](https://github.com/JieWaZi/acp-go-sdk) 维护，模块路径为 `github.com/JieWaZi/acp-go-sdk`。原生客户端使用了其中的接纳、取消、写入回执和背压接口，不能直接换成未经修改的上游版本。SDK 接口变更须先发布可下载版本，再更新 acp-go 与宿主的固定依赖。

## 在 ACP 客户端中使用

将 `command` 设为 `acp-agent` 的绝对路径，在 `args` 中选择适配器，并将 `cwd` 设为项目目录。Gemini 与 WorkBuddy 将原生进程绑定到启动目录，ACP 会话使用同一目录。客户端应分别读取协议 stdout 与诊断 stderr。

```json
{
  "command": "/absolute/path/to/acp-agent",
  "args": ["--adapter", "codex"],
  "cwd": "/absolute/path/to/project"
}
```

CLI 由用户安装并按厂商流程登录。可选执行路径使用 `CODEX_PATH`、`CLAUDE_CODE_EXECUTABLE`、`CURSOR_PATH`、`PI_PATH`、`WORKBUDDY_PATH`、`QODER_PATH`、`QODER_CN_PATH`、`KIMI_PATH`、`GEMINI_PATH`、`GROK_PATH` 或 `OPENCODE_PATH`。Pi 的 MCP bridge 通过 `PI_MCP_MODULE_PATH` 配置。

客户端按初始化能力发送 `mcpServers`、Elicitation 和会话操作。原生会话、模型、权限与用量以各 CLI 的握手和回执为准。

## 作为 Go 包使用

使用各包的 `NewClient(ctx, Options, HostCallbacks)`。构造冻结 CLI、前置参数和完整环境，`Initialize` 执行启动和能力协商。`Environment: nil` 继承当前环境。宿主提供消息观察和交互回调，结束使用时关闭客户端。

直接 Go 调用通过 `ReservePrompt` 获取执行函数，并读取独立的输入接纳、原生终态、用量和交付错误。协议服务使用 `NewACPAgent` 构造共享客户端，再交给 `acpserver`。`cmd/acp-agent/main.go` 使用的也是这条链路：按 `--adapter` 选择各包的 `NewClient`，由 `NewACPAgent` 投影为 ACP，再由 `acpserver` 管理 stdio 服务生命周期。

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

`Serve` 在断线或取消时关闭客户端；取消时同时结束自身 ACP 逻辑连接，断线时保留已收通知的排空。输入输出由调用方持有，调用方负责解除仍在进行的物理读写。仅在真实握手确认的范围内使用原生 fork、恢复、模型设置和权限模式。连续对话只发送本轮新增输入。

## 开发与验证

适配器和服务入口的常规测试使用 fake CLI 与冻结报文。按修改范围运行对应包。

```sh
GOWORK=off go test ./pkg/agentclient ./pkg/acpclient ./pkg/acpserver ./cmd/acp-agent
GOWORK=off go test ./pkg/codex ./pkg/claude ./pkg/cursor
GOWORK=off go test ./pkg/pi ./pkg/workbuddy ./pkg/qoder ./pkg/standardacp
GOWORK=off go run ./tools/protocolgen --check
```

共享层测试覆盖子进程、启动参数冻结、请求接纳、取消、通知顺序、背压和资源回收；命令及服务测试覆盖适配器选择、stderr 诊断、ACP 回执、问答与退出。各厂商包保存对应的 fake CLI、冻结报文及行为测试。真实 CLI、账号、模型及跨平台运行须单独验收。

Codex 生成协议用 `go generate ./pkg/codex/protocol` 刷新。厂商来源见各包的 `UPSTREAM.md`，标准 ACP 来源见 [NATIVEACP-UPSTREAM.md](pkg/standardacp/NATIVEACP-UPSTREAM.md)。
