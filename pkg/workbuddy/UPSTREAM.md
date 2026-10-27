# WorkBuddy 协议来源与兼容基线

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

默认回归使用实际子进程 fake CLI 和两层 SDK：零私有元数据、完整环境、版本补齐、认证公开元数据、模型和思考原始 ID、布尔配置解析、MCP 原样挂载、new/load、两轮新增输入、审批、单选/多选/自由回答、拒答/取消、错误码原文、公开消息元数据和不支持的 steering。共享 nativeacp 的 FIFO、代际取消、文件/终端回调、进程回收与错误原文回归继续适用。

`TestInstalledWorkBuddyProtocol` 以 `WORKBUDDY_REAL_ACP=1` 显式开启，在临时 `CODEBUDDY_CONFIG_DIR` 与工作目录中验证实际 CLI 握手、新建、原生 auto 权限及跨进程 load，不登录、不发送 Prompt。真实服务商的模型回复、工具执行、问答、Teams/MCP-UI 和 Windows 进程树不由 fixture 或无模型探针证明。

升级时重新检查零身份元数据路径、问答能力位和请求/回执、模型/思考菜单、布尔配置、权限重设、MCP 刷新、load 重放、取消和进程退出。公开 Teams 的 `memberEvent` 必须由消费它的宿主按成员归属解释，不把它视为主智能体的身份声明。

CodeBuddy Code 2.151.0 的实际 CLI 入口 `dist/codebuddy.js` 使用 `handleToolApproval`：`AskUserQuestion` 通过 `session/request_permission` 携带 `toolCall._meta["codebuddy.ai/toolName"]` 和 `toolCall.rawInput.questions`。普通 `allow` 只放行工具，随后 `AskService.acquireAnswers` 等待答案，不能作为问答完成。适配器在此边界转为标准 Form Elicitation，并调用 `_codebuddy.ai/resolveInterruption`，参数为 `{sessionId,toolCallId,decision:"allow"|"deny",answers:{[questionText]:string|string[]}}`；必须收到 `{resolved:true}`，旧审批回执随之取消。只有用户实际提交的答案才形成 `allow`，拒答走 `deny`，执行取消不提交迟到答案。`WORKBUDDY_REAL_QUESTION=1 go test ./pkg/workbuddy -run '^TestInstalledWorkBuddyQuestion$' -count=1 -v` 在临时目录执行真实模型连续两轮问答，检查回答表单、无普通授权及模型收到真实答案；此检查会使用当前登录账号。
