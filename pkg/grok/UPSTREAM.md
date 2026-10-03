# Grok Build 上游基线

固定来源：[官方仓库 官方公开源码快照（未有 release）](https://github.com/xai-org/grok-build/tree/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8)，commit `2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`。这里只适配原生 stdio，不执行 acpx，也不下载 CLI。第三方完整源码缓存放在忽略的 `.upstream/native-adapters/grok`，不随包发布。

核对路径：crates/codegen/xai-grok-shell/src/{agent/session_config,agent/handlers/config_option,agent/mvp_agent/acp_agent,extensions/interject,session/auto_mode,util/config/resolve/auto_mode,util/grok_home}.rs。

| 项目 | 基线事实 |
| --- | --- |
| 传输 | ACP SDK 管理请求 ID、通知顺序和双向回调；stdout 仅协议，stderr 仅诊断 |
| 模型 | 实际 configOptions 为准；匹配 legacy models 的逐模型 metadata 补充目录，仅支持标记为 true 且无菜单时复现固定上游五档 fallback |
| 版本 | Adapter 使用 buildinfo，实际 CLI 版本位于 `agentInfo._meta.runtime.version` |
| 生命周期 | 每会话 FIFO Prompt；取消移除旧代排队项，原生 close 不支持时保留 MethodNotFound |

[acpx v0.19.4](https://github.com/openclaw/acpx/tree/v0.19.4) 仅参考其 registry/process/model-config 状态惯例，不作为执行依赖。

升级时：固定新版本到 SHA，重新检查入口、capabilities、模型/configOptions/setter、权限模式、会话存储与账号路径；更新冻结协议 fixture；运行本包行为测试、相关 race 测试和全仓检查。无账号协议测试不证明真实模型或第三方端点兼容。完整覆盖边界见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。

原生 auto 是 `session/auto_mode.rs` 的风险分类器，非 yolo 的别名。`util/config/resolve/auto_mode.rs` 按 requirements → `GROK_AUTO_PERMISSION_MODE` → config → managed → remote 顺序决策；Go 请求 `autoMode=true` 不绕过 kill switch。`extensions/interject.rs` 的回执只有 `status: queued`；统一 steering 保留这一语义，不伪造 injected。公开快照未有 release，二进制实际版本仍需要调用者记录与固定。

逐模型目录的源码映射（均为上述固定 SHA）：

| 事实 | 精确来源 |
| --- | --- |
| legacy ModelInfo._meta 提供支持标记、当前 effort、逐模型菜单 | [agent/config.rs:4888](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/config.rs#L4888) |
| 菜单序列化含输入 id、规范 value、label、description、default | [sampling-types/src/types.rs:915–929、1046](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-sampling-types/src/types.rs#L915) |
| model select 原生只保留 value/name；当前菜单/未列出 currentValue 保留 | [agent/session_config.rs:120–173](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/session_config.rs#L120) |
| New/Load/Resume 同时携带 models 与 configOptions | [session_setup.rs:916、1381、2016](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/mvp_agent/session_setup.rs#L916) |
| 当前模型 overlay、输入 id resolver、支持标记 gate 和空菜单 fallback | [agent_ops.rs:3280、3382、3415–3431](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/mvp_agent/agent_ops.rs#L3280) |
| 支持 getter 直接读取模型标记；菜单 getter 不做 fallback | [remote_config/manager/mod.rs:545–568](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/remote_config/manager/mod.rs#L545) |
| fallback 固定 minimal/low/medium/high/xhigh，标签 Minimal/Low/Medium/High/X-High，default=false | [session_config.rs:7、62–77](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/session_config.rs#L7) |

端点/认证也是源码事实，尚未做真实 Grok 或付费端点端到端验证：`api_backend` 的 snake_case 枚举 `chat_completions` / `responses` / `messages` 见 [sampling-types/src/types.rs:1104](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-sampling-types/src/types.rs#L1104)；独立 `auth_scheme` 的 `bearer` / `x_api_key` 见 [sampler/src/config.rs:20](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-sampler/src/config.rs#L20)，`x-api-key` 请求头和 messages 客户端分支见 [sampler/src/client.rs:514、2181](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-sampler/src/client.rs#L514)。[ConfigModelOverride:3461–3483](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/config.rs#L3461) 的 TOML override 支持 `api_backend`，但不含 `auth_scheme`；协议枚举不证明此配置路径能覆盖认证。模型专属 `api_key` / `env_key` 与 API-key 路由的 `api_base_url` 需和实际供应方端点匹配。全局 `XAI_API_KEY` 示例只适用于 xAI 第一方凭据。
