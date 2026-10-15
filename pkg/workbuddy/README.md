# WorkBuddy / CodeBuddy Code

通过已安装的 `codebuddy --acp` 接入 WorkBuddy 执行能力；未配置路径且 `codebuddy` 不存在时尝试同一产品的 `cbc --acp`。这是标准 stdio ACP，不依赖 WorkBuddy 桌面端、daemon 或私有 owner 注入接口。

```sh
acp-agent --adapter workbuddy
# 可选：指定实际 CodeBuddy Code 程序
WORKBUDDY_PATH=/absolute/path/to/cbc acp-agent --adapter workbuddy
```

作为 Go 包使用：

```go
agent, err := workbuddy.NewAgent(ctx, workbuddy.Config{
    Logger: logger,
    WorkBuddyPath: "/absolute/path/to/codebuddy",
    Environment: os.Environ(),
    WorkingDirectory: "/absolute/path/to/workspace",
    PermissionMode: "default",
})
if err != nil { return err }
server, err := acpserver.NewWithUserInput(agent, os.Stdin, os.Stdout)
if err != nil { return err }
return server.Serve(ctx)
```

`PrefixArgs` 位于固定 `--acp` 参数之前。完整环境同时用于 PATH 解析、进程启动和版本探测；显式无效路径不回退。配置目录使用原生 `CODEBUDDY_CONFIG_DIR`，鉴权沿用 CLI 自己的登录或 `CODEBUDDY_API_KEY`；中国版 Key 还需要 `CODEBUDDY_INTERNET_ENVIRONMENT=internal`，iOA 为 `ioa`，海外版默认不设置。适配器不安装 CLI、不发起登录、不写用户默认配置。

| 能力 | 行为 |
| --- | --- |
| 初始化、认证 | 原样转发标准 ACP 和公开 `codebuddy.ai/userinfo`；CLI 版本放入 `agentInfo._meta.runtime.version` |
| 会话 | 标准 new/load；resume、list、close、fork 只以当前 CLI 握手为准，不伪造能力；无 close 时取消并释放本地映射，进程退出回收资源 |
| 连续输入与取消 | 每会话 FIFO；只发送本轮内容，不重复拼接历史、指令或 MCP；取消会使待回答表单和迟到答案失效 |
| 模型与思考 | 原样保留模型 ID；标准 `thought_level` 规范为 `reasoning`，写入时还原原生 ID；保留布尔配置和其他公开选项，不推测其他模型的思考档位 |
| 权限 | `default` → `default`、`auto` → 原生 AI 分类模式、`full-access` → `bypassPermissions`；构造时冻结并在 new/load/resume 后重设，未声明的模式明确失败；空值保留 CLI 默认 |
| 提问 | `_codebuddy.ai/question` → 标准 Form Elicitation；复用 `pkg/userinput` 的单选、多选、自由回答、自定义选项、拒答和取消；完全访问仍须用户回答，不重复注入问答 MCP |
| 文件、终端、工具、diff、计划 | 复用标准 ACP 客户端回调及会话更新；只在宿主声明能力后由 CLI 使用 |
| MCP、图片、用量 | STDIO 和声明的 HTTP/SSE 配置原样交付；图片、Prompt Usage、`usage_update` 按原生回执转发，不制造缺失统计 |
| 公共扩展 | 保留标准事件上的公开 `_meta`，其他 `_codebuddy.ai/*` 方法继续转发给显式调用方/宿主 |

标准基础链路不需要任何 `codebuddy.ai/*` 身份 `_meta`。只有宿主声明表单交互时，在 `clientCapabilities._meta["codebuddy.ai"].question` 声明公开问答能力。不得自行构造 `sessionAdmissionV2`、`authSession`、`runtimeTransportCredential`、`sessionGrantV1` 等内部材料。

WorkBuddy 的 Teams、Workflow、MCP-UI 等公开扩展仍由需要这些功能的宿主解释；当前包不创建额外团队界面、不调用私有准入或凭据刷新接口。统一即时 steering 未提供可靠回执，明确不支持；排队 Prompt 不等于即时插话。未声明的额外目录或音频能力不会被补成支持。

```sh
GOWORK=off go test ./pkg/workbuddy ./pkg/nativeacp ./cmd/acp-agent -count=1
GOWORK=off go test -race ./pkg/workbuddy ./pkg/nativeacp -count=1
# 显式探测真实已安装 CLI：隔离配置，只握手、建会话和冷恢复，不发送模型 Prompt
GOWORK=off WORKBUDDY_REAL_ACP=1 go test ./pkg/workbuddy -run TestInstalledWorkBuddyProtocol -v
```

协议来源、固定版本和验证边界见 [UPSTREAM.md](UPSTREAM.md)。
