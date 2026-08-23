---
generated_from_state_version: 14
---

# Verification

## Current result

- Result: **Passed**
- Assurance: **skill-coordinated**
- Goal cycle: 1
- Iteration: 3
- Verifier attempt: 1
- Completed: 2026-08-23T19:21:53.438Z
- Summary: PASS：A1-A35 全部通过。真实 delayed turn/start count=50、observer/cancel/fatal/duplicate race count=20、串行 full test/race、vet、protocol freshness 和跨平台构建均通过。

## Acceptance

| ID | Result | Source | Criterion | Reason |
| --- | --- | --- | --- | --- |
| A1 | passed | brief.md | A1：显式无效 `CODEX_PATH` 不回退，PATH 缺失返回诊断；非 0.148.0 只警告并继续，stdout 不受污染。 | CODEX_PATH/PATH、版本警告与 stdout 隔离正确。 |
| A2 | passed | brief.md | A2：ACP initialize 只触发一次 app-server initialize，随后 new/resume/load 可建立隔离 session。 | initialize once 与 new/resume/load 顺序正确。 |
| A3 | passed | brief.md | A3：同一 session 连续多轮 prompt；early completion、旧 turn completion 与跨 session 事件不会错误结束当前 prompt。 | 多轮及 early/stale/cross-session completion 正确。 |
| A4 | passed | brief.md | A4：cancel-before-start 不留下 turn；迟到 turn/start 被标记 stale 并 interrupt；重复取消幂等。 | cancel-before-start 的迟到 turn stale 且 interrupt-once。 |
| A5 | passed | brief.md | A5：并发 steering 严格 FIFO，无活动 turn 时启动新 turn，单项失败后队列继续且不产生 rival turn。 | steering FIFO、失败隔离与单 control turn 正确。 |
| A6 | passed | brief.md | A6：app-server EOF/exit、session close 与 Adapter close 会解除 pending request/waiter/queue，无 goroutine 泄漏。 | close/fatal 解除本地 owner；迟到 observer 有 transport owner 并清理 server turn。 |
| A7 | passed | brief.md | A7：`go test ./...`、`go test -race ./...`、`go vet ./...` 与目标构建通过。 | test/race/vet/freshness/build 通过。 |
| A8 | passed | brief.md | A8：全部手写 Go 类型、字段和函数有中文注释，关键并发逻辑注明固定 codex-acp symbol/test 来源。 | 中文注释和 UPSTREAM 并发差异追踪完整。 |
| A9 | passed | specs/codex-runtime/spec.md | Codex 可执行文件由用户预装；若 `CODEX_PATH` 非空则只使用该显式路径，否则使用 `exec.LookPath` 或等价机制从 PATH 查找。显式路径无效或 PATH 不存在 Codex 时返回清晰启动错误，不下载或静默回退。 | 显式路径失败不回退，PATH 诊断清晰。 |
| A10 | passed | specs/codex-runtime/spec.md | 启动前获取 Codex 版本；0.148.0 是已验证基线。其他版本在 stderr/结构化日志输出兼容性警告后继续尝试，stdout 不得出现非 ACP JSON-RPC 内容。 | 0.148.0 基线与非基线告警正确。 |
| A11 | passed | specs/codex-runtime/spec.md | Codex Adapter 使用解析到的 Codex 启动 `codex app-server` 子进程，只使用 stdin/stdout JSON-RPC；V1 不增加 HTTP、WebSocket 或 Unix Socket 传输。 | 唯一 app-server 与 stdio 无 jsonrpc NDJSON 正确。 |
| A12 | passed | specs/codex-runtime/spec.md | 启动后执行 app-server initialize，再允许 thread/turn 请求。 | initialize 后才开放 thread/turn。 |
| A13 | passed | specs/codex-runtime/spec.md | stdout 只承载协议消息；stderr 进入结构化/关联日志，不能污染 JSON-RPC。 | stderr 实时结构化记录且不污染 stdout。 |
| A14 | passed | specs/codex-runtime/spec.md | 子进程退出、stdin/stdout 断开或 Adapter 关闭时，dispose 连接、取消活动请求并向 ACP 返回稳定错误；清理必须幂等。 | EOF/exit/Close fatal fanout 与幂等清理正确。 |
| A15 | passed | specs/codex-runtime/spec.md | new session 映射到 Codex thread/start，建立 ACP SessionID 与 Codex ThreadID 的内部关系。 | session/new 映射 thread/start。 |
| A16 | passed | specs/codex-runtime/spec.md | load/resume 使用固定上游基线的恢复流程，恢复必要历史与配置后才安装 session 状态。 | resume/load 使用 resume→read→install。 |
| A17 | passed | specs/codex-runtime/spec.md | session open/close 使用 generation 或等价身份保护：迟到的 open 结果不能覆盖已关闭或重新打开的 session。 | generation fence 阻止迟到安装。 |
| A18 | passed | specs/codex-runtime/spec.md | 一个 session 的失败、关闭或恢复错误不得污染其他 session。 | session 与 completion 隔离正确。 |
| A19 | passed | specs/codex-runtime/spec.md | prompt 将 Text/Image/Resource 转换后的输入提交到 turn/start，并从对应 thread/turn 路由流式事件。 | Text/Image/Resource 与当前 turn 路由正确。 |
| A20 | passed | specs/codex-runtime/spec.md | 同一 session 可连续多轮；每个 prompt 只由自己 turn 的 completion 结束一次。 | 每轮由自身 completion exactly-once 结束。 |
| A21 | passed | specs/codex-runtime/spec.md | completion 可能早于等待器建立；实现必须捕获并匹配 early completion。 | response 前 completion 可捕获。 |
| A22 | passed | specs/codex-runtime/spec.md | stale turn notification 不得完成当前 prompt；旧 turn 的等待器和状态在确定完成/取消后清理。 | 旧 turn/stale 通知不结束当前 prompt。 |
| A23 | passed | specs/codex-runtime/spec.md | prompt 请求在 turn 启动前取消时不得留下后台 rival turn；若 turn 已活动，使用 Codex turn interrupt。 | 启动前取消立即释放前台且迟到服务端 turn 不 orphan。 |
| A24 | passed | specs/codex-runtime/spec.md | interrupt 后等待对应 completion/cancelled 语义完成清理；重复取消幂等。 | active interrupt、completion-first 与重复取消正确。 |
| A25 | passed | specs/codex-runtime/spec.md | session/进程结束会解除所有取消等待，不允许 goroutine 泄漏。 | session/process/Adapter 清理有明确 owner 与终点。 |
| A26 | passed | specs/codex-runtime/spec.md | 每个 session 一个 FIFO steering queue，同一时间只处理一个 steering 请求。 | 每 session 单 consumer FIFO。 |
| A27 | passed | specs/codex-runtime/spec.md | 有活动 turn 时发送 turn/steer；若上游返回“无活动 turn”竞态或 session 本来空闲，则按固定基线规则以 steering 内容启动新 turn。 | active steer 与 no-active fallback 正确。 |
| A28 | passed | specs/codex-runtime/spec.md | 并发 steering 不得启动 rival turn；请求在“注入活动 turn”或“新 turn 已被接受”后返回成功。 | 并发 steering 无 rival turn。 |
| A29 | passed | specs/codex-runtime/spec.md | 一个 steering 的解析、发送、启动或取消失败只拒绝该请求，队列继续处理后续请求。 | 单项失败不阻塞后续请求。 |
| A30 | passed | specs/codex-runtime/spec.md | session 关闭时拒绝/清理未完成请求并移除空闲队列。 | session close 清理 pending steering。 |
| A31 | passed | specs/codex-runtime/spec.md | thread、turn、session generation/identity 必须参与 completion、notification、approval 和 control 路由。 | notification/approval/completion/control 校验完整 generation identity。 |
| A32 | passed | specs/codex-runtime/spec.md | early completion、stale completion、stale approval、close/open race 和 app-server 异常退出均有固定测试。 | early/stale approval、close/open、process exit 均有固定测试。 |
| A33 | passed | specs/codex-runtime/spec.md | A3：进程与 JSON-RPC 生命周期。 | 进程与内层 JSON-RPC 生命周期完整。 |
| A34 | passed | specs/codex-runtime/spec.md | A4-A7：session、prompt、cancel 和 steering。 | session/prompt/cancel/steering 主链路完整。 |
| A35 | passed | specs/codex-runtime/spec.md | A13：early/stale/exit 竞态保护。 | early/stale/exit 竞态保护完整。 |

## Checks

| Check | Command | Working directory | Status | Exit | Duration |
| --- | --- | --- | --- | ---: | ---: |
| Runtime full tests with hard timeout | 240s go test ./... -count=1 -timeout=210s | . | passed | 0 | 25742 ms |
| Runtime full race tests with hard timeout | 450s go test -race ./... -count=1 -timeout=420s | . | passed | 0 | 31327 ms |
| Go vet | 120s go vet ./... | . | passed | 0 | 650 ms |
| Protocol freshness | 180s go run ./tools/protocolgen --check | . | passed | 0 | 3390 ms |
| Build ACP agent without worktree artifact | 120s go build -o /tmp/acp-agent-runtime-v3 ./cmd/acp-agent | . | passed | 0 | 744 ms |

## Blockers

_None._

## Risks and skipped work

- 未执行需要真实账号/凭据的可选 Codex E2E；真实 NDJSON 主链路由 fake app-server wire 覆盖。

## Previous iterations

| Goal cycle | Iteration | Attempt | Outcome | Unresolved | Summary | Completed |
| ---: | ---: | ---: | --- | --- | --- | --- |
| 1 | 1 | 1 | fail | A4, A6, A8, A13, A23, A25, A31, A32, A33, A34 | FAIL：stderr 未实时进入结构化日志；cancel-before-start/session close 对卡住的 turn/start 不能释放 active slot/pending RPC；A31/A32 与 sibling approval non-goal 存在合同冲突。其余 fresh checks 与主要 runtime 行为通过。 | 2026-08-23T15:35:58.078Z |
| 1 | 2 | 1 | fail | A6, A8, A32, A34 | FAIL：CloseSession 取消本地 pending call 时删除真实 wire observer，迟到 turn/start 无法按 fixed upstream stale+interrupt，可能留下 app-server orphan turn；其余 fresh checks 与 A1-A35 项通过。 | 2026-08-23T18:50:34.970Z |
| 1 | 3 | 1 | pass | — | PASS：A1-A35 全部通过。真实 delayed turn/start count=50、observer/cancel/fatal/duplicate race count=20、串行 full test/race、vet、protocol freshness 和跨平台构建均通过。 | 2026-08-23T19:21:53.438Z |

## Conclusion

PASS：A1-A35 全部通过。真实 delayed turn/start count=50、observer/cancel/fatal/duplicate race count=20、串行 full test/race、vet、protocol freshness 和跨平台构建均通过。
