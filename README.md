# acp-go

`acp-go` 是一个使用 Go 实现的 [Agent Client Protocol（ACP）](https://agentclientprotocol.com/) stdio Agent 服务。它通过可选择的 Adapter，把 ACP 客户端请求转交给具体的 Agent 运行时，并将运行时事件映射回统一的 ACP 会话、内容和工具调用模型。

当前 V1 提供 Codex 与 Claude Adapter：Codex 使用用户本机已安装的 [OpenAI Codex CLI](https://github.com/openai/codex) `app-server`；Claude 使用用户本机已安装且已登录的 [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code)，由每个 ACP Session 独立持有一个 stream-json 进程。

> `codex` 始终是默认 Adapter；只有显式传入 `--adapter claude` 才会探测和构造 Claude。

## 特性

- 基于 `github.com/coder/acp-go-sdk` 提供标准 ACP stdio 服务，协议输出与诊断日志严格分离。
- 使用显式 Adapter 注册表组织实现，启动时可通过 `--adapter` 选择，未知 Adapter 会在建立协议连接前失败。
- 支持 Codex 会话的新建、恢复、加载、关闭，多轮 Prompt、取消和 `_session/steering`。
- 支持文本、图片、嵌入上下文和资源链接输入。
- 支持 ChatGPT 浏览器登录与 API Key 认证，并避免在日志和错误中泄露凭据。
- 支持模型、推理强度以及 `read-only`、`agent`、`agent-full-access` 三种运行模式。
- 映射消息、推理、计划、Token Usage、命令、文件变更、MCP 工具和终端输出事件。
- 映射命令执行、文件变更和细粒度权限审批；取消、异常或过期请求默认拒绝。
- 使用固定 Schema 生成强类型 Codex app-server DTO，并提供可重复的协议新鲜度检查。
- Claude 支持 new/load/resume/close、多轮 FIFO Prompt、cancel、steering、文本/图片/资源、工具、权限、计划、usage、model/effort/fast/mode、additional directories 和客户端 stdio/HTTP/SSE MCP。

Codex Adapter 的实现边界、调用链和维护约束见 [`internal/codex/README.md`](internal/codex/README.md)。
Claude 的固定版本、协议声明、fixture 和 Go 文件映射统一见 [`UPSTREAM.md`](UPSTREAM.md)。

## 架构

```mermaid
flowchart LR
    Client[ACP 客户端] <-->|ACP / stdio| Server[acp-agent]
    Server --> Registry[Adapter Registry]
    Registry --> Codex[Codex Adapter]
    Registry --> Claude[Claude Adapter]
    Codex <-->|强类型 NDJSON| AppServer[用户预装的 Codex CLI<br/>app-server]
    Claude <-->|stream-json / control| ClaudeCLI[用户预装的 Claude Code CLI<br/>每 Session 一个进程]
```

`acp-agent` 只在 `stdout` 读写 ACP 协议帧，启动错误、版本警告和 Codex 诊断信息统一写入 `stderr`，避免污染协议流。

## 快速开始

### 环境要求

- Go `1.25.8`，以 [`go.mod`](go.mod) 为准。
- 使用 Codex Adapter 时，用户自行安装 Codex CLI。当前验证基线是 `0.148.0`；其他可识别版本会输出兼容性警告，然后继续尝试启动。
- 使用 Claude Adapter 时，用户自行安装并登录 Claude Code CLI；版本探测只用于诊断，不作为硬性门槛。
- 仅在刷新或重新生成 Codex 协议类型时需要 Node.js 与 npm；构建和运行已提交代码不依赖它们。

先确认 Codex 可执行且能输出版本：

```sh
codex --version
```

然后构建 Agent：

```sh
git clone https://github.com/JieWaZi/acp-go.git
cd acp-go
go build -o ./acp-agent ./cmd/acp-agent
```

`acp-agent` 由 ACP 客户端作为子进程启动，并通过 stdin/stdout 通信。直接运行下面的命令时，进程会等待客户端发送协议输入：

```sh
./acp-agent --adapter codex
```

`codex` 是默认 Adapter，因此也可以省略 `--adapter codex`。

Claude 需要显式选择：

```sh
./acp-agent --adapter claude
```

不同 ACP 客户端的配置格式并不完全相同，核心启动信息如下：

```json
{
  "command": "/absolute/path/to/acp-go/acp-agent",
  "args": ["--adapter", "codex"],
  "env": {
    "CODEX_PATH": "/absolute/path/to/codex"
  }
}
```

如果 `codex` 已在客户端进程的 `PATH` 中，可以不设置 `CODEX_PATH`。

Claude 客户端配置使用 `"args": ["--adapter", "claude"]`；如果 `claude` 已在 `PATH` 中，无需设置环境变量，否则可设置 `CLAUDE_CODE_EXECUTABLE` 为绝对路径。

## 运行时配置

| 环境变量 | 作用 |
| --- | --- |
| `CODEX_PATH` | 指定 Codex 可执行文件。非空时只使用该路径，路径无效不会回退到 `PATH`；未设置时才从 `PATH` 查找 `codex`。 |
| `CODEX_API_KEY` | API Key 认证使用的首选环境变量。 |
| `OPENAI_API_KEY` | `CODEX_API_KEY` 未设置时使用的后备 API Key。 |
| `NO_BROWSER` | 任意非空值都会隐藏 ChatGPT 浏览器认证入口，适合无图形界面的运行环境。 |
| `CLAUDE_CODE_EXECUTABLE` | 指定 Claude Code 可执行文件。非空时只使用该路径，路径无效不会回退到 `PATH`；未设置时才查找 `claude`。 |
| `CLAUDE_CONFIG_DIR` | 可选 Claude 配置目录；Claude load 历史回放会从其 `projects` 子目录查找 transcript。 |

API Key 的完整选择顺序是：ACP 认证请求中的 `_meta.api-key.apiKey`、`CODEX_API_KEY`、`OPENAI_API_KEY`。

## 项目结构

| 路径 | 职责 |
| --- | --- |
| [`cmd/acp-agent`](cmd/acp-agent) | 命令行入口、进程 stdio 边界和 Adapter 组合根。 |
| [`internal/acpserver`](internal/acpserver) | 使用 ACP SDK 建立 Agent 侧连接，并管理 Adapter 的有界关闭。 |
| [`internal/core`](internal/core) | 与具体实现无关的 Adapter 注册和选择。 |
| [`internal/codex`](internal/codex) | Codex app-server 到 ACP 的运行时适配。 |
| [`agents/codex/protocol`](agents/codex/protocol) | Codex app-server Schema、生成 DTO 和受控 Envelope。 |
| [`internal/claude`](internal/claude) | Claude per-session 进程、Session、事件、权限与配置适配。 |
| [`agents/claude/protocol`](agents/claude/protocol) | Claude stream-json/control 窄类型与冻结 fixture。 |
| [`tools/protocolgen`](tools/protocolgen) | 固定工具链下的协议生成、新鲜度和稳定面检查。 |
| [`UPSTREAM.md`](UPSTREAM.md) | 上游版本、源码映射、Fixture 对照和已知差异。 |
| [`docs/V1_TEST_MATRIX.md`](docs/V1_TEST_MATRIX.md) | V1 能力对应的默认测试与真实 Codex 测试矩阵。 |
| [`docs/CLAUDE_V1_TEST_MATRIX.md`](docs/CLAUDE_V1_TEST_MATRIX.md) | Claude V1 fake CLI 证据与真实 CLI smoke 边界。 |

## 开发与验证

默认测试使用可控的 fake app-server 和 fake Claude CLI，不需要账号凭据，也不会产生模型费用：

```sh
go test ./... -count=1 -timeout=300s
go vet ./...
go run ./tools/protocolgen --check
```

涉及并发、生命周期或协议边界的变更，还应运行竞态检查：

```sh
go test -race -p=1 ./... -count=1 -timeout=600s
```

真实 Codex Smoke Test 和完整 V1 场景见 [`docs/V1_TEST_MATRIX.md`](docs/V1_TEST_MATRIX.md)；Claude 的真实 CLI smoke 边界见 [`docs/CLAUDE_V1_TEST_MATRIX.md`](docs/CLAUDE_V1_TEST_MATRIX.md)。两者都是显式开启的本机状态/潜在付费测试。

## 协议同步

Codex app-server 的稳定 Schema、生成工具和参考实现版本均已固定。不要手工修改 `agents/codex/protocol/generated_protocol.go`。

```sh
# 从已提交 Schema 重新生成 Go 类型
go generate ./agents/codex/protocol

# 检查已提交产物是否与固定输入一致
go run ./tools/protocolgen --check

# 刷新 Codex Schema 并重新生成；需要 Node.js/npm
go run ./tools/protocolgen --refresh-schema
```

升级上游或扩大 V1 协议面前，请先阅读 [`UPSTREAM.md`](UPSTREAM.md)，并同步更新源码映射、Fixture 证据和测试。

## V1 边界

- 默认 Adapter 是 `codex`；Claude 只能通过 `--adapter claude` 显式选择。
- 项目不捆绑、下载或自动安装 Codex/Claude CLI，也不修改用户的登录状态。
- Claude V1 不实现 session list/fork/delete、认证/登出、terminal、elicitation、providers、goal 和 Audio 输入；完整清单见 [`UPSTREAM.md`](UPSTREAM.md)。
- Codex V1 会映射已产生的 MCP 工具事件；Claude V1 还会把客户端 stdio/HTTP/SSE MCP 配置传给该 Session 的 CLI。
