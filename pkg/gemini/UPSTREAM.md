# Gemini CLI 上游基线

固定来源：[官方仓库 v0.62.0](https://github.com/google-gemini/gemini-cli/tree/b460678f3db508407554afd604cc9d6635becb2a)，commit `b460678f3db508407554afd604cc9d6635becb2a`。这里只适配原生 stdio，不执行 acpx，也不下载 CLI。第三方完整源码缓存放在忽略的 `.upstream/native-adapters/gemini`，不随包发布。

核对路径：packages/cli/src/acp/{acpRpcDispatcher,acpSession,acpSessionManager,acpUtils}.ts；packages/core/src/{services/modelConfigService,config/defaultModelConfigs,config/storage,utils/paths}.ts；packages/cli/src/config/settings.ts。

| 项目 | 基线事实 |
| --- | --- |
| 传输 | ACP SDK 管理请求 ID、通知顺序和双向回调；stdout 仅协议，stderr 仅诊断 |
| 模型 | 原生 catalog 或官方 legacy models，不硬编码供应商模型菜单 |
| 版本 | Adapter 使用 buildinfo，实际 CLI 版本位于 `agentInfo._meta.runtime.version` |
| 生命周期 | 每会话 FIFO Prompt；取消移除旧代排队项，原生 close 不支持时保留 MethodNotFound |

[acpx v0.19.4](https://github.com/openai/acpx/tree/v0.19.4) 仅参考其 registry/process/model-config 状态惯例，不作为执行依赖。

升级时：固定新版本到 SHA，重新检查入口、capabilities、模型/configOptions/setter、权限模式、会话存储与账号路径；更新冻结协议 fixture；运行本包行为测试、相关 race 测试和全仓检查。无账号协议测试不证明真实模型或第三方端点兼容。完整覆盖边界见 [测试矩阵](../../docs/NATIVE_ACP_TEST_MATRIX.md)。

思考配置当前仍为候选研究，不公开 setter。候选值以 [官方 GenerateContent thinking 文档](https://ai.google.dev/gemini-api/docs/generate-content/thinking)（2026-10-03）交集固定 core catalog；2.5 使用真实数值 budget，3.x 使用真实 level，未知/auto 模型不猜测。官方 resolver/SDK 序列化 proof：

```sh
GEMINI_OFFICIAL_PROOF_DIRECTORY=/absolute/path/to/official-npm-fixture go test ./pkg/gemini -run TestOfficialResolverAndSDK -count=1 -v
```

fixture 需安装官方 `@google/gemini-cli-core@0.62.0` 和 `@google/genai@1.30.0`，测试截获 fetch 不发 HTTP。27 个生成 preset 的参数通过 resolver/SDK，但官方 CLI 实际模型路由及 continuation 会丢 alias。`packages/core/src/core/client.ts` 与 `packages/cli/src/acp/acpSession.ts` 证明续轮复用 canonical currentSequenceModel；BeforeModel hook translator 不支持 thinkingConfig，system settings 又要求 root 所有权。需要独立会话配置/原生历史恢复的真正多回合 proof 后才能宣称 thinking 生效。官方同分钟 load 还存在 transcript filename 碰撞风险，恢复 shim 与测试待兼容层补完。
