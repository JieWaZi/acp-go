# OpenCode 上游基线

固定来源：[官方仓库 v1.18.34](https://github.com/anomalyco/opencode/tree/aec0b9a6d8898f68f923aaf08b7306d931fd9d76)，commit `aec0b9a6d8898f68f923aaf08b7306d931fd9d76`。这里只适配原生 stdio，不执行 acpx，也不下载 CLI。第三方完整源码缓存放在忽略的 `.upstream/native-adapters/opencode`，不随包发布。

核对路径：packages/opencode/src/acp/service.ts、config-option.ts、event.ts、permission.ts、directory.ts；packages/core/src/global.ts；packages/opencode/src/auth/index.ts。

| 项目 | 基线事实 |
| --- | --- |
| 传输 | ACP SDK 管理请求 ID、通知顺序和双向回调；stdout 仅协议，stderr 仅诊断 |
| 模型 | 原生 catalog 或官方 legacy models，不硬编码供应商模型菜单 |
| 版本 | Adapter 使用 buildinfo，实际 CLI 版本位于 `agentInfo._meta.runtime.version` |
| 生命周期 | 每会话 FIFO Prompt；取消移除旧代排队项，原生 close 不支持时保留 MethodNotFound |

[acpx v0.19.4](https://github.com/openai/acpx/tree/v0.19.4) 仅参考其 registry/process/model-config 状态惯例，不作为执行依赖。

升级时：固定新版本到 SHA，重新检查入口、capabilities、模型/configOptions/setter、权限模式、会话存储与账号路径；更新冻结协议 fixture；运行本包行为测试、相关 race 测试和全仓检查。无账号协议测试不证明真实模型或第三方端点兼容。完整覆盖边界见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。

自动审批的无工具路径逐项核对 `packages/opencode/src/effect/runtime-flags.ts`（PURE/default plugins）、`plugin/index.ts`、`config/{config,paths,managed}.ts`（来源与 managed 策略）、`tool/registry.ts`（自定义工具动态 import）、`session/llm/request.ts` 与 `permission/index.ts`（最终模型工具过滤）、`cli/cmd/run.ts`（stdin/agent/model/json）。配置中的模型级 `provider.npm` 与顶层 npm 都检查官方模块白名单；私有 HOME/TEST_HOME/XDG/CONFIG_DIR 避免扫描用户可执行文件。原始配置来源无法精确重建时返回人工审批，绝不默默更换账号。

官方 CLI loopback proof 可选运行：

```sh
OPENCODE_OFFICIAL_PROOF_BINARY=/absolute/path/to/pinned/opencode go test ./pkg/opencode -run TestOfficialOpenCodeReviewer -count=1 -v
```

该测试禁用官方模型目录抓取，模型请求仅访问本地 HTTP fixture，验证实际 provider 请求模型、零 advertised tools、严格 reviewer 结果，并证明调用者 `.opencode/tools` 的 sentinel 模块未执行。不证明真实 OAuth 或账号成功。
