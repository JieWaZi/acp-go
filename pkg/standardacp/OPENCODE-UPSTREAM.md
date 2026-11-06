# OpenCode 上游基线

下文保留固定上游和旧适配器的证据。当前实现及测试入口见 [开发与验证](../../README.md#开发与验证)。

固定来源：[官方仓库 v1.18.34](https://github.com/anomalyco/opencode/tree/aec0b9a6d8898f68f923aaf08b7306d931fd9d76)，commit `aec0b9a6d8898f68f923aaf08b7306d931fd9d76`。这里只适配原生 stdio，不执行 acpx，也不下载 CLI。第三方完整源码缓存放在忽略的 `.upstream/native-adapters/opencode`，不随包发布。

核对路径：packages/opencode/src/acp/service.ts、config-option.ts、event.ts、permission.ts、directory.ts；packages/core/src/global.ts；packages/opencode/src/auth/index.ts。

| 项目 | 基线事实 |
| --- | --- |
| 传输 | ACP SDK 管理请求 ID、通知顺序和双向回调；stdout 仅协议，stderr 仅诊断 |
| 模型 | 原生 catalog 或官方 legacy models，不硬编码供应商模型菜单 |
| 版本 | Adapter 使用 buildinfo，实际 CLI 版本位于 `agentInfo._meta.runtime.version` |
| 生命周期 | 每会话 FIFO Prompt；取消移除旧代排队项，原生 close 不支持时保留 MethodNotFound |

## Prompt 用量

`prompt_usage.go` 只将已核对版本的 ACP `usage` 映射为消费计数；其他版本或总数不一致的响应保留为范围未知的观察值。两版原生输入都不含缓存，原生输出都不含推理；规范输出包含推理，缓存读取和写入独立保存。原生实现省略恰好为零的可选计数，因此仅在这些固定版本补零。

| 原生版本 | 计数覆盖 | 规范覆盖 |
| --- | --- | --- |
| 1.18.34 | 最后一条助手消息 | `observed_requests`，已知小计 |
| [2.0.29](https://github.com/anomalyco/opencode/tree/042d0832f5f3a73eaa039a58a7d565776f6d4290) | 当前根会话本轮已报告的模型步骤，含失败步骤的已报告用量，不含子会话步骤 | `main_loop`，已知小计 |

2.0.29 的 [ACP 翻译](https://github.com/anomalyco/opencode/blob/042d0832f5f3a73eaa039a58a7d565776f6d4290/packages/cli/src/acp/translate.ts) 按本轮重新初始化并累加根会话步骤；[原生用量归一化](https://github.com/anomalyco/opencode/blob/042d0832f5f3a73eaa039a58a7d565776f6d4290/packages/core/src/session/usage.ts) 分别读取非缓存输入、可见输出、推理、缓存读写。ACP Prompt 回执没有实际消费模型标识，适配器不以当前配置替代消费归属。

本机 2.0.29 通过原生 ACP 连续调用 `openrouter/openai/gpt-4o-mini`，首轮报告输入 4629、输出 2；次轮报告非缓存输入 40、缓存读取 4608、输出 2，与该测试会话的原生导出一致。两轮均无工具调用，推理和缓存写入均为零；正推理、正缓存写入、多步骤和子会话边界来自固定源码与回归样例，未声称真实模型覆盖这些路径。

[acpx v0.19.4](https://github.com/openclaw/acpx/tree/v0.19.4) 仅参考其 registry/process/model-config 状态惯例，不作为执行依赖。

升级时：固定新版本到 SHA，重新检查入口、capabilities、模型/configOptions/setter、权限模式、会话存储与账号路径；更新冻结协议 fixture；运行本包行为测试、相关 race 测试和全仓检查。无账号协议测试不证明真实模型或第三方端点兼容。完整覆盖边界见 [开发与验证](../../README.md#开发与验证)。

自动审批的无工具路径逐项核对 `packages/opencode/src/effect/runtime-flags.ts`（PURE/default plugins）、`plugin/index.ts`、`config/{config,paths,managed}.ts`（来源与 managed 策略）、`tool/registry.ts`（自定义工具动态 import）、`session/llm/request.ts` 与 `permission/index.ts`（最终模型工具过滤）、`cli/cmd/run.ts`（stdin/agent/model/json）。配置中的模型级 `provider.npm` 与顶层 npm 都检查官方模块白名单；私有 HOME/TEST_HOME/XDG/CONFIG_DIR 避免扫描用户可执行文件。原始配置来源无法精确重建时返回人工审批，绝不默默更换账号。

官方 CLI loopback proof 可选运行：

```sh
OPENCODE_OFFICIAL_PROOF_BINARY=/absolute/path/to/pinned/opencode go test ./pkg/opencode -run TestOfficialOpenCodeReviewer -count=1 -v
```

该测试禁用官方模型目录抓取，模型请求仅访问本地 HTTP fixture，验证实际 provider 请求模型、零 advertised tools、严格 reviewer 结果，并证明调用者 `.opencode/tools` 的 sentinel 模块未执行。不证明真实 OAuth 或账号成功。
