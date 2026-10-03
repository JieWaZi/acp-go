# Gemini CLI 上游基线

固定来源：[官方 v0.62.0](https://github.com/google-gemini/gemini-cli/tree/b460678f3db508407554afd604cc9d6635becb2a)，SHA `b460678f3db508407554afd604cc9d6635becb2a`。适配器只启动已安装 CLI，使用现有 `coder/acp-go-sdk`；不执行 acpx，不下载或内嵌完整 CLI。[acpx v0.19.4](https://github.com/openclaw/acpx/tree/v0.19.4) 仅为 registry/process/config 状态惯例参考。

| 项目 | 固定源码与适配行为 |
| --- | --- |
| ACP 入口、模型与 MCP | `packages/cli/src/acp/{acpRpcDispatcher,acpSession,acpSessionManager,acpUtils}.ts`；真实 legacy catalog，官方 canonical set_model，stdio/HTTP/SSE MCP |
| 会话设置生命周期 | `acpSessionManager.ts` new 重新加载 cwd 设置，load 使用进程启动设置；每公共会话独立进程，进程 cwd 使用解析符号链接后的会话路径 |
| 思考 | `packages/core/src/{services/modelConfigService,config/defaultModelConfigs,core/client}.ts`；官方 resolver 的 canonical override，先 null 清旧字段再设置 level 或 budget；不使用 continuation 会失效的 alias |
| 能力快照 | [官方 GenerateContent thinking 文档](https://ai.google.dev/gemini-api/docs/generate-content/thinking)，2026-10-03；精确 ID 与实际 native catalog 取交集。3.8 支持已核对 low/medium/high；3.5 路由提升会改变实际模型，故不显示可选思考档位 |
| 设置优先级 | `packages/cli/src/config/settings.ts`；默认层、user、可信 workspace、system 依次合并。私有 user 不绕过 root-owned system；更高层 modelConfigs 导致显式思考选择拒绝 |
| 历史 | `packages/core/src/services/chatRecordingService.ts`、`packages/cli/src/utils/sessionUtils.ts`、`packages/core/src/utils/sessionUtils.ts`；同分钟初始化会追加 messages reset，需在 load 前保护真实 JSONL；遵循官方启动上下文过滤，最新有效记录逐字节复制，失败回滚 |
| 关闭与恢复 | 原生无 close/resume，本包以独占子进程释放实现 CloseSession，以真实 Load 并抑制回放实现 Resume；稳定公共身份持久映射到官方身份 |
| 审批 | 官方 approval-mode default/yolo；auto 使用 headless 零工具分类器与共享 allow_once/audit 门禁。`tools.core=[]`、hooks/admin 功能关闭、独立 profile 与 cwd；统计必须确认当前模型 |
| 信任与账号 | `packages/core/src/{config/storage,utils/paths,utils/trust}.ts`；私有 user settings；仅信任审查器自己创建的临时目录；显式 restricted/false 与系统配置边界回退人工；默认 profile 只读 |
| 版本 | Adapter 用 `internal/buildinfo.Current()`，真实 CLI version 放在既有 acpmeta runtime metadata |

可选官方验证直接调用生产 Go Adapter 和审查器，使用固定 npm `@google/gemini-cli@0.62.0` 的 `package/bundle/gemini.js`，模型请求仅发往本地 HTTP fixture，不使用真实凭据：

```sh
GEMINI_OFFICIAL_CLI=/absolute/path/to/package/bundle/gemini.js \
  go test ./pkg/gemini -run 'TestOfficialSessionThinkingAndHistory|TestOfficialReviewerNoTools' -count=1 -v
```

会话测试必须观察真实 `read_file` tool_call 和 completed 更新、工具响应出现在下一次生成请求、所有请求 canonical 3.8、首轮和续轮 LOW、替换恢复后的 MEDIUM、再次 default 后恢复原始 HIGH 与 temperature，并验证历史和公共 callback ID。审查器测试检查每次真实生成请求没有工具声明及返回统计中的准确模型。普通测试使用 fake CLI + real SDK；实际 OAuth、计费模型、第三方端点与外部 MCP 不在这些证明范围内。CLI 内部启动行为不是网络沙箱。

升级时固定新 SHA，重核入口、catalog、精确思考能力、路由提升、配置合并、原生记录格式、权限和信任路径；运行 fixture、并发 race、以上实际 CLI loopback 和全仓检查。固定版本的历史规避不能在未经重新验证时视为其他版本保证。
