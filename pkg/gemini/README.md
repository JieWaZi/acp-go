# Gemini CLI Adapter

`gemini.NewAgent(ctx, gemini.Config{Logger: logger})` 使用已安装的官方 `gemini --acp`，命令入口是 `acp-agent --adapter gemini`。`GeminiPath` 或命令入口的 `GEMINI_PATH` 指定安装路径；`PrefixArgs` 放在 `--acp` 前，可传官方 `--include-directories`。固定基线为 0.62.0，不下载 CLI，也不重试旧版 experimental 入口。

每个公共会话拥有独立原生子进程。模型目录来自实际 `availableModels/currentModelId`，转为标准 `model` 配置；非空准确模型 ID 仍可经官方 setter 使用，但不会被添加到供应商目录。模型选项的 `acp-go/model-config-options` 元数据携带该实际模型的精确思考选项，供宿主无需切换模型即可读取。

`reasoning` 只显示固定官方能力与实际目录的交集：2.5 模型使用数值 budget，已核对的 3.x 模型使用 level，3.8 flash 提供 low/medium/high。未知、auto 与固定 CLI 会提升到其他模型的 3.5 flash 不获得推测的档位。`default` 恢复启动时读取的原始生成设置；显式档位清空互斥的旧 budget/level，再写入 canonical model override。思考内容依然作为 `agent_thought_chunk` 单独转发。本轮真实 quota token 计数映射标准 usage，不推测上下文占用率。

**模型和思考修改只接受空闲会话。** 适配器准备新进程、初始化、加载真实持久历史、应用模型和模式，全部成功后才交换执行代。忙碌时返回明确错误，不取消当前轮次。公共身份及回调归属保持稳定；空会话可由新的官方 session ID 重建，并保存公共到原生身份映射。替换需要一次 CLI 启动，会增加配置切换延迟。

原生 JSONL 历史逐字节保护，避免固定 CLI 的同分钟 load 初始化覆盖；候选按日志更新时间、文件更新时间和路径稳定排序，失败恢复原字节。Load 可回放真实历史，Resume 抑制历史回放。CloseSession 通过释放该会话独占进程实现真正关闭，保留持久记录；父 Close 与进程崩溃回收相应临时配置和等待请求。每会话 FIFO 与取消覆盖已经接受的排队请求。固定 CLI 没有即时 steering，本包不声明支持。MCP stdio/HTTP/SSE 由官方 CLI 实现。

`PermissionMode` 支持 `default`、`auto`、`full-access`。default 保留官方默认审批；full-access 使用官方 yolo。auto 保持原生 default 门禁，用同一 CLI、当前精确模型在临时目录执行零工具风险审查，只能批准 `allow_once`，并发送既有 permission-review 审计。拒绝、错误、证据缺失、模型提升/回退、取消均回退宿主或取消审批。auto 不等于官方 auto_edit。

审查器禁用工具、hooks、MCP、extensions、skills，只信任其自己创建的临时工作目录；真实项目与用户信任设置不变。显式 restricted/false trust 或非空系统设置使其保守转人工。工作区或系统 `modelConfigs` 可能高于私有 user 配置；存在这些配置时显式思考修改返回错误，不提交无法保证生效的选择。不会使用临时 SYSTEM 设置、提权或绕过系统策略。

账号隔离示例：

```go
environment := nativeacp.WithEnvironment(os.Environ(), "GEMINI_CLI_HOME", "/managed/account-a/gemini")
environment = nativeacp.WithEnvironment(environment, "GEMINI_API_KEY", accountKey)
agent, err := gemini.NewAgent(ctx, gemini.Config{
    Logger: logger, Environment: environment,
    WorkingDirectory: "/managed/project-a", PermissionMode: "auto",
})
```

`GEMINI_CLI_HOME` 是根目录，其下才是 `.gemini`。显式受管 home 保存原生状态；`StateDirectory` 可另外指定持久根。未指定受管 home 时，仅读取默认用户配置和凭据，状态保存在按有效 profile 和工作区区分的适配器缓存。临时 user settings 保留原始生成设置；新凭据、认证刷新、原生项目注册表与历史保留在受管状态，清理不会回写真实默认 profile。显式 Authenticate 输入会重放到新的会话子进程。受管状态拒绝符号链接写入目标，文件原子更新使用受限根目录；无法持久化时返回错误并保留临时目录供恢复。

`Environment == nil` 继承宿主，非 nil 完全替换（包括空 slice）；参数和环境均克隆。构造请求取消不终止已成功创建的长期进程；调用者通过 Close 管理生命周期。诊断只使用 stderr/logger，stdout 保持 ACP 协议。

来源与可复现的官方无账号 loopback 验证见 [UPSTREAM.md](UPSTREAM.md)，覆盖边界见[测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。真实 OAuth、计费模型与外部 MCP 尚未验证。
