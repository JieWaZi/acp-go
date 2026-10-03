# Grok Build 上游基线

固定来源：[官方仓库 官方公开源码快照（未有 release）](https://github.com/xai-org/grok-build/tree/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8)，commit `2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`。这里只适配原生 stdio，不执行 acpx，也不下载 CLI。第三方完整源码缓存放在忽略的 `.upstream/native-adapters/grok`，不随包发布。

核对路径：crates/codegen/xai-grok-shell/src/{agent/session_config,agent/handlers/config_option,agent/mvp_agent/acp_agent,extensions/interject,session/auto_mode,util/config/resolve/auto_mode,util/grok_home}.rs。

| 项目 | 基线事实 |
| --- | --- |
| 传输 | ACP SDK 管理请求 ID、通知顺序和双向回调；stdout 仅协议，stderr 仅诊断 |
| 模型 | 原生 catalog 或官方 legacy models，不硬编码供应商模型菜单 |
| 版本 | Adapter 使用 buildinfo，实际 CLI 版本位于 `agentInfo._meta.runtime.version` |
| 生命周期 | 每会话 FIFO Prompt；取消移除旧代排队项，原生 close 不支持时保留 MethodNotFound |

[acpx v0.19.4](https://github.com/openai/acpx/tree/v0.19.4) 仅参考其 registry/process/model-config 状态惯例，不作为执行依赖。

升级时：固定新版本到 SHA，重新检查入口、capabilities、模型/configOptions/setter、权限模式、会话存储与账号路径；更新冻结协议 fixture；运行本包行为测试、相关 race 测试和全仓检查。无账号协议测试不证明真实模型或第三方端点兼容。完整覆盖边界见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。

原生 auto 是 `session/auto_mode.rs` 的风险分类器，非 yolo 的别名。`util/config/resolve/auto_mode.rs` 按 requirements → `GROK_AUTO_PERMISSION_MODE` → config → managed → remote 顺序决策；Go 请求 `autoMode=true` 不绕过 kill switch。`extensions/interject.rs` 的回执只有 `status: queued`；统一 steering 保留这一语义，不伪造 injected。公开快照未有 release，二进制实际版本仍需要调用者记录与固定。
