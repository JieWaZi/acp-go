# Protocol generation child complete target specification

## Fixed input and output

- 固定 Codex 0.148.0 默认稳定 app-server JSON Schema；不传 `--experimental`。
- 生成独立 Go protocol package，保持 request/response/notification、wire field、optional/nullable 和 tagged union 语义。
- 生成文件含 `Code generated` 标记、来源和上游原始文档，不手工编辑或维护中文翻译层。

## Reproducibility

- 仓库提供确定性生成命令和 freshness 检查；相同输入必须产生逐字节一致输出。
- 生成快照随仓库提交；schema 变化必须先再生类型，再修改运行时、mapper 和测试。

## Upstream traceability

- 项目根目录下的 `.upstream/codex-acp` 必须是 HEAD 为 `ba5bcc3d7759250dde9d4d2286a1bec11b363208` 的本地 Git clone。
- `pkg/codex/UPSTREAM.md` 固定 acp-go-sdk v0.13.5、codex-acp 1.6.2、ACP TS SDK 1.4.0、Codex 0.148.0，并列出纳入/跳过模块、关键 TS→Go 映射、等价改写和同步步骤。
- 实现前直接读取 clone 中对应源文件、symbol 与 fixture；不得仅依赖 README 或摘要重造协议语义。
