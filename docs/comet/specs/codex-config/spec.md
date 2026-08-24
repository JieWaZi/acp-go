# Codex configuration and input complete target specification

## Session configuration

- initialize/session 创建时声明并接受固定基线支持的 model、reasoning effort 与 sandbox/agent mode 选项。
- Adapter 将 ACP config option/mode 映射为 Codex app-server 参数；未知、失效或不兼容值返回可诊断错误，不静默使用危险权限。
- sandbox/agent mode 映射遵循固定上游语义，尤其保持 read-only、workspace-write/agent 和 full-access 的权限边界。
- load/resume 对配置的恢复/覆盖顺序与固定上游基线一致。

## Prompt input

- V1 接受 ACP Text、Image 和 Resource input，并转换为固定 app-server schema 的 turn input。
- Image 保留可用 MIME/URL/base64 信息；Resource 支持固定基线可转换的 resource link 与 embedded resource。
- 不支持的 content block 类型以明确请求错误处理，不丢弃后继续；Audio 不在 V1。

## Authentication

- initialize 声明 ChatGPT 与 API Key 两种基础认证方式。
- ChatGPT 登录和 API Key 环境/请求的选择、成功、取消和失败按固定上游基线映射。
- V1 不支持 client-provided custom gateway/Gateway Auth；不得误宣告该能力。
- 凭据不得写入日志、`pkg/codex/UPSTREAM.md`、fixture 或错误详情。

## Acceptance mapping

- A11：model/reasoning/sandbox/mode。
- A12：Text/Image/Resource 与 ChatGPT/API Key。
