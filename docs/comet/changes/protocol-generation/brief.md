# Outcome

交付 Codex 0.148.0 稳定 app-server JSON Schema 到 Go 的确定性协议生成链路、提交的生成快照、freshness 检查和 `UPSTREAM.md`，为运行时与事件子变更提供唯一的强类型 wire contract，并建立对本地固定 codex-acp clone 的逐模块追溯入口。

# Scope

- 使用父工作区 `/Users/ryan/Desktop/aix/acp-go/.upstream/codex-acp` 的本地 clone，验证 HEAD 为 `ba5bcc3d7759250dde9d4d2286a1bec11b363208`。
- 固定并提交 Codex 0.148.0 默认稳定 app-server JSON Schema 输入；不包含 experimental surface。
- 实现受控、确定性的 JSON Schema→Go 生成器和生成命令，输出独立 protocol package。
- 生成文件保留 `Code generated` 标记、来源与上游原始文档，禁止手工修改。
- 提供 freshness 检查，确保相同 schema 输入无差异再生。
- 编写 `UPSTREAM.md`：版本、纳入/跳过范围、codex-acp TS→Go 职责映射、等价改写和同步流程。

# Non-goals

- 不实现 Codex 子进程、session/turn、事件 mapper、审批或配置行为。
- 不生成 experimental API，不为生成声明补写中文翻译元数据。
- 不 vendoring 完整 codex-acp 仓库；本地 clone 由 `.gitignore` 排除。

# Acceptance examples

- A1：固定稳定 schema 可通过受控命令重复生成可编译 Go 类型；连续两次生成结果一致，freshness 检查通过。
- A2：生成文件带标准禁止手改标记和精确来源，手写代码不重复声明已生成 DTO，experimental 类型不进入 V1 surface。
- A3：`UPSTREAM.md` 精确记录 SDK、codex-acp、Codex/schema 版本以及关键 TS 文件、symbol、fixture 到 Go 目标职责的映射。
- A4：本地 `.upstream/codex-acp` clone 的 HEAD 校验通过，实现依据来自该 clone 的源码而不是二手摘要。
- A5：protocol package 测试、`go test ./...`、`go vet ./...` 和生成 freshness 检查通过。

# Constraints and invariants

- 继承父变更 `acp-multi-agent-codex-go-v1` 的已确认 brief 与 `specs/protocol-upstream/spec.md`。
- Codex schema 基线固定 0.148.0；codex-acp 固定 1.6.2/`ba5bcc3d7759250dde9d4d2286a1bec11b363208`。
- 相同输入必须稳定输出；生成文件不适用手写中文注释规则。
- 生成器优先复用成熟库，只有仓库确无满足稳定 union/nullable/wire 需求的库时才实现薄生成逻辑。

# Decisions

- 2026-08-23：父 Shape 已授权本子变更，不重复请求用户确认。
- 2026-08-23：稳定 Protocol Generator 是 V1 硬门槛，不能延期。
- 2026-08-23：必须直接读取本地 codex-acp clone 并保留可审计映射。

# Open questions

- 无。

# Verification expectations

- 校验本地 upstream clone HEAD。
- 连续运行生成命令与 freshness 检查，确认工作树无差异。
- 运行格式化、`go test ./...`、`go vet ./...`。
- 审查生成 surface 不含 experimental，`UPSTREAM.md` 映射可用于后续增量同步。
