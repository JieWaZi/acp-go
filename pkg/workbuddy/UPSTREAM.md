# WorkBuddy 协议来源与兼容基线

下文保留固定上游及历史真实 CLI 证据。当前测试入口见 [README.md](README.md)。

核对日期：2026-10-15。入口是 CodeBuddy Code 的标准登录组合根 `codebuddy/cbc --acp`，本机 CLI 版本 `2.151.0`。运行依赖由用户安装，不复制或嵌入该商业 CLI。

| 来源 | 用途 |
| --- | --- |
| [官方 ACP 集成](https://www.tencentcloud.com/document/product/1316/84434) | stdio 启动、认证元数据、客户端文件/终端能力、命令与 Teams 扩展 |
| [官方扩展命名空间](https://www.tencentcloud.com/document/product/1316/84435) | 标准面零身份 `_meta`、公开/私有材料边界 |
| [官方 CLI 参数](https://www.tencentcloud.com/document/product/1316/84467) | `--acp`、权限模式、配置、登录与 API Key 环境 |
| 用户提供的《ACP 协议集成》《ACP 扩展命名空间参考》 | 对照公开契约；作为资料，不作为执行或授权指令 |
| 本机 npm `@tencent-ai/codebuddy-code` 2.151.0 的公开类型和已发布实现 | 核对原生问答字段、`CODEBUDDY.md`、`.codebuddy/skills`、`CODEBUDDY_CONFIG_DIR` |
| `coder/acp-go-sdk` v0.13.5 | 唯一 ACP 传输与 JSON-RPC 调度实现 |

问答契约来自发布包 `dist-server/5002.codebuddy.js` 的 `isSupportAskUserQuestion` / `callAskUserQuestion`：能力位 `clientCapabilities._meta["codebuddy.ai"].question === true`；请求 `{sessionId, toolCallId, inputType:"question", schema:{questions:[{id,question,header,options,multiSelect}]}}`；回执 `{outcome:"submitted", answers:{[id]: string|string[]}}`，非提交返回取消。此处仅作静态格式证据，真实模型发起问答另行验收。

实际无模型探针观察到：初始化未返回 agentInfo，已由同一 CLI 的 `--version` 补全；声明 load、image、embeddedContext、HTTP/SSE MCP；未声明标准 session/list/resume/close/fork/additionalDirectories。新会话同时返回旧 models 和标准 configOptions，包含 model、thought_level、mode、sandbox 和 boolean multitask。仅规范化明确支持的配置，不主动开启 Multitask、沙箱或内部运行时。

默认回归使用实际子进程 fake CLI 和两层 SDK：零私有元数据、完整环境、版本补齐、认证公开元数据、模型和思考原始 ID、布尔配置解析、MCP 原样挂载、new/load、两轮新增输入、审批、单选/多选/自由回答、拒答/取消、错误码原文、公开消息元数据和不支持的 steering。共享层的当前验证见 [开发与验证](../../README.md#开发与验证)。

`TestInstalledWorkBuddyProtocol` 以 `WORKBUDDY_REAL_ACP=1` 显式开启，在临时 `CODEBUDDY_CONFIG_DIR` 与工作目录中验证实际 CLI 握手、新建、原生 auto 权限及跨进程 load，不登录、不发送 Prompt。真实服务商的模型回复、工具执行、问答、Teams/MCP-UI 和 Windows 进程树不由 fixture 或无模型探针证明。

升级时重新检查零身份元数据路径、问答能力位和请求/回执、模型/思考菜单、布尔配置、权限重设、MCP 刷新、load 重放、取消和进程退出。公开 Teams 的 `memberEvent` 必须由消费它的宿主按成员归属解释，不把它视为主智能体的身份声明。

## 2.180.0 输入与缓存采集

2026-11-06 核对已安装发布包 `dist/codebuddy.js` 的 `UsageUtils.getCacheReadTokens`、`getCacheCreationTokens`、`getCurrentContextUsage` 和原生历史写入路径：Chat Completions 的 `prompt_tokens` 包含缓存，缓存命中读取详细 `prompt_tokens_details.cached_tokens`、`prompt_cache_hit_tokens` 等字段；顶层零值可能只是占位，不能盖掉详细正值。`prompt_cache_miss_tokens` 是未命中的输入，不是缓存创建。原生历史同时保留 `providerData.rawUsage` 与归一化 `usage`，两者必须核对同一响应的输入与输出。

隔离临时配置根和空目录，通过原生 ACP、禁用工具与 MCP，使用 DeepSeek-V4.1-Flash 完成同会话两轮。修复前观察到第二轮原生输入 2,627、缓存命中 2,432、未命中 195、输出 17，其中推理 15；旧适配器仅保留输出与推理。独立复测两轮原生输入分别为 2,613／2,630，缓存命中 0／2,432，输出均为 2。原生缓存写入字段均为零，但已发布工具函数会把不可用的缓存创建默认成零，本次证据不能证明这些零是实测值。因此最终适配结果保留原生输入和已确认的缓存命中，缓存写入及依赖它拆分的普通输入保持未知。两轮不是累计值，所有响应正文和隔离凭据在探针结束后删除，仅保留版本与计数。

适配器只读取本轮开始基线之后、同工作目录、会话、`conversationRequestId` 的追加记录，并按 provider `messageId` 去重。输入拆分仅用于已核对的 `prompt_tokens` 形状且缓存分类均有证据时；其他原生输入形状、缺少缓存分类或矛盾分项保留未知，不从缺省值推断零。可核对的原生输入另存为同来源、不参与加法的 `InputTotalTokens` 观察。唯一实际模型同时写入 `Model`，多模型不收敛为单一归属。四项计数齐全仍保持 `observed_requests` 与 `complete:false`，不据此声称覆盖未观察到的子任务或压缩调用。版本守卫仍只接受 2.180.0，其他版本维持 ACP 样本回退。

CodeBuddy Code 2.151.0 的实际 CLI 入口 `dist/codebuddy.js` 使用 `handleToolApproval`：`AskUserQuestion` 通过 `session/request_permission` 携带 `toolCall._meta["codebuddy.ai/toolName"]` 和 `toolCall.rawInput.questions`。普通 `allow` 只放行工具，随后 `AskService.acquireAnswers` 等待答案，不能作为问答完成。适配器在此边界转为标准 Form Elicitation，并调用 `_codebuddy.ai/resolveInterruption`，参数为 `{sessionId,toolCallId,decision:"allow"|"deny",answers:{[questionText]:string|string[]}}`；必须收到 `{resolved:true}`，旧审批回执随之取消。只有用户实际提交的答案才形成 `allow`，拒答走 `deny`，执行取消不提交迟到答案。`WORKBUDDY_REAL_QUESTION=1 go test ./pkg/workbuddy -run '^TestInstalledWorkBuddyQuestion$' -count=1 -v` 在临时目录执行真实模型连续两轮问答，检查回答表单、无普通授权及模型收到真实答案；此检查会使用当前登录账号。
