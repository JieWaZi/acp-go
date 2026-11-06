# WorkBuddy / CodeBuddy Code

`workbuddy.NewClient` 使用原生 `codebuddy --acp`。缺省发现失败时尝试 `cbc --acp`。标准 ACP 连接和执行状态由 [acpclient](../acpclient) 管理。厂商包处理模型、权限和原生问答。直接 Go 调用与 `acp-agent --adapter workbuddy` 共用该客户端。

使用原生 `CODEBUDDY_CONFIG_DIR` 和厂商支持的认证环境。显式凭据通过 `Options.ExplicitCredential` 禁用交互式账号切换。模型目录默认只读。逐模型发现如需临时切换模型，宿主应提供隔离的 `Options.CatalogConfigHome`。原生进程与会话共享同一工作目录。

定向验证使用 `go test ./pkg/workbuddy ./pkg/acpclient`。上游及实测版本证据见 [UPSTREAM.md](UPSTREAM.md)。
