# 原生 ACP 进程接入

`pkg/kimi`、`pkg/cursor`、`pkg/pi` 提供现有 Adapter 风格的 Config、NewAgent 和 Agent，可直接交给 acpserver.New。公共层使用 coder/acp-go-sdk 的 Connection、请求关联、通知排序、取消与类型校验，不包含第二套 JSON-RPC 实现。

```go
agent, err := kimi.NewAgent(ctx, kimi.Config{
    Logger: logger,
    KimiPath: "/absolute/path/to/kimi",
    Environment: os.Environ(),
    WorkingDirectory: "/absolute/path/to/project",
})
if err != nil { return err }
server, err := acpserver.New(agent, os.Stdin, os.Stdout)
if err != nil { return err }
return server.Serve(ctx)
```

WorkingDirectory 是进程启动目录，session/new.cwd 是会话目录；需要固定 cwd 的进程应对应一个工作区。命令从所传完整环境的 PATH 解析，nil 环境继承宿主。PrefixArgs 位于 Kimi/Cursor 的 acp 子命令前，Pi 独立包将参数放在官方 RPC 参数之前。不会安装、升级程序或登录。

公共层只规范化标准 `category=model` 和思考类别，并通过窄接口组合厂商适配器，不保存任何 CLI 私有状态。Cursor 的参数化模型、私有回调与交互通知全部由 `pkg/cursor` 拥有；旧 Kimi `models/set_model`、Python 工具参数和自动审批全部由 `pkg/kimi` 拥有。发现过程不循环切换模型。

各厂商包对外统一返回标准 ACP：Cursor 问答经标准表单 Elicitation，计划经一次性审批并包含完整正文；无法唯一关联活跃会话时取消。待办、task/image 通知投影到标准会话更新。Pi 包将 pi-acp 的协议行为移植为 Go 并内置开源 MCP 工厂，具体权限边界见各包 README。

会话列表、load、resume、additionalDirectories、图片等沿用上游 initialize，不能把方法存在当作能力支持。MCP HTTP/SSE 以握手为准；Pi 声明内置扩展提供的传输。宿主必须展示并如实回传审批选项。

未声明 close 的上游收到取消，本地映射删除，资源随进程退出回收。Close 先结束 stdin，再有界终止进程；acpserver.Serve 负责调用。Unix 终止独立进程组，包含 Pi / MCP 子进程。其他平台目前只保证直接子进程清理，未做 Windows 进程树实测。

[来源基线](UPSTREAM.md) · [验证记录](../../docs/NATIVE_ACP_TEST_MATRIX.md)
