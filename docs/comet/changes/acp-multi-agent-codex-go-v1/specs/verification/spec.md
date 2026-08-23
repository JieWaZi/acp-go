# Verification complete target specification

## Test layers

- 单元测试验证 Core registry/capability、session state、content/event/tool mapping、approval decision 和 SteeringQueue。
- 并发测试用显式同步点覆盖 concurrent steering、cancel-before-start、interrupt、early completion、stale event/approval、session close/open generation 和 process exit；不得以长 sleep 作为正确性依据。
- JSON-RPC fake app-server 集成测试覆盖 initialize、thread start/resume、turn stream/completion、server request/response、stderr/exit 和协议错误。
- 从固定 codex-acp 基线移植纳入 V1 的关键 fixture/测试语义；记录未移植测试及其对应非目标。
- 对照 acp-go-sdk 公共 API 审查协议边界，发现重复 connection/dispatch/cancel/extension 实现即视为失败。
- 可控环境下运行固定版本真实 `codex app-server` 的关键路径 smoke/integration；认证或平台前置条件缺失时明确记为 not-run/blocked，不伪造通过。

## Required commands

- 完成全部本地检查后，把实现、测试、生成快照与文档提交到本地 Git；本变更不要求 remote、push 或 PR 证据。
- macOS 与 Linux 运行 Go 格式化与生成文件 freshness 检查。
- 从固定 Codex 0.148.0 稳定 schema 重新运行 Protocol Generator，确认工作树无生成差异。
- macOS 与 Linux 运行 `go test ./...`、`go test -race ./...`、`go vet ./...` 并构建目标 ACP Agent 二进制。
- Windows 仅运行可行的交叉编译或编译检查；其专项运行和真实 app-server 集成不属于 V1 门槛。

## Coverage contract

- 每个 A1-A17 验收项都有至少一个自动化检查或明确的人工/环境验证证据。
- unknown event、fail-closed、child process exit 和 stale/early completion 是发布门槛，不得仅测试成功路径。
- “完整 E2E/跨平台专项不在 V1”不等于不做真实关键路径验证：macOS 与 Linux 是正式支持平台，任何一个平台的必需检查失败都会阻止发布；Windows 是 best-effort 编译兼容，结果作为证据或已知限制记录。
- 代码审阅检查所有手写类型、结构体字段和函数的中文注释，以及关键状态/并发/异常逻辑的上游来源说明；生成代码仅检查生成标记、上游原始文档和 freshness，不检查中文覆盖。
- 代码审阅检查每个抽象是否存在当前消费方、第二实现或明确测试边界；仅服务假想未来的抽象视为超范围。

## Acceptance mapping

- A17：完整验证分层与发布证据。
- A1-A16：各 capability 的具体验证载体。
- A18：中文注释、增量同步可维护性和必要扩展性审查。
