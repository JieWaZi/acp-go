# Protocol generation and upstream alignment complete target specification

## Version contract

- V1 在 `UPSTREAM.md` 固定 acp-go-sdk v0.13.5/`0845a3bb9eddda5bfc22a94dd3598c90cb842451`、codex-acp 1.6.2/`ba5bcc3d7759250dde9d4d2286a1bec11b363208`，以及 codex-acp lockfile 解析的 `@agentclientprotocol/sdk` 1.4.0 和 `@openai/codex` 0.148.0。
- 开发和验收期间不使用 moving main 作为行为真值；后续上游修复或版本升级通过独立变更更新版本、映射、生成类型和回归证据。
- V1 固定使用 Codex 0.148.0 的默认稳定 app-server schema；不传 `--experimental`，不生成、不宣告也不调用实验 surface。
- 固定基线之后，纳入范围的行为、异常兜底、顺序和竞态测试优先于 Go 风格重设计。
- acp-go-sdk 已提供的 ACP 连接、分发、取消和扩展能力直接复用其公共实现；除适配所必需的薄边界外，不复制内部协议逻辑。

## Go protocol generation

- 候选默认是固定 Codex app-server 的稳定 JSON Schema，由 `codex app-server generate-json-schema --out <dir>` 产生输入；实验 surface 仅在明确纳入时使用。
- 生成流程输出 `agents/codex/protocol/generated_*.go` 或等价独立 package，保持 wire field 名、nullable/optional、union/tag 和 request/response/notification 类型。
- generated 文件带生成来源和禁止手改标记；手写 Adapter 只依赖生成类型，不复制大批 DTO。
- generated 文件保留上游 schema 原始注释，不维护逐类型/字段的中文翻译元数据；中文注释规范仅检查手写 Go 代码。
- 仓库提供一个可重复命令和 freshness 验证；相同输入产生稳定输出，schema 变化先更新类型再修 mapper/state/tests。
- Protocol Generator、固定输入、提交的 Go 生成快照和 freshness 检查均为 V1 发布硬门槛；任何一项缺失都不能把 A15 标记为通过。

## Traceability

- 开发工作区必须存在 `.upstream/codex-acp` 本地 Git clone，且 HEAD 精确等于 `ba5bcc3d7759250dde9d4d2286a1bec11b363208`；该目录由 `.gitignore` 排除，不把完整 TypeScript 上游仓库 vendoring 到本项目提交中。
- Builder 在实现 Codex 模块或移植测试前直接读取对应 clone 中的源文件、symbol 和 fixture；README 或摘要只能辅助定位，不能替代源码对照。
- `UPSTREAM.md` 列出主要 TypeScript→Go 职责映射、明确跳过的模块/事件、Go 等价改写说明和每次同步记录。
- 关键 concurrency/failure 代码注释指出对应 upstream symbol/test 或记录有意差异，不要求逐行翻译。
- codex-acp 中存在对应行为、状态机、mapper 或 fixture 时，Go 代码保持可识别的文件/职责映射并优先等价移植；不得在没有 Spec 决策的情况下另造替代语义。
- 未纳入 V1 的上游模块整体排除，不修改相邻核心语义来制造简化版本。
- `UPSTREAM.md` 的映射结构和关键中文注释应让维护者可以从新的 codex-acp 修复定位受影响 Go 文件和测试。

## Acceptance mapping

- A15：可重复类型生成与 freshness。
- A16：版本、模块、差异和同步可追溯。
- A13：竞态语义来源。
- A18：中文注释中的上游来源和复杂逻辑说明。
