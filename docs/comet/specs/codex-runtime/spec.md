# Codex runtime complete target specification

## Process and JSON-RPC lifecycle

- Codex 可执行文件由用户预装；若 `CODEX_PATH` 非空则只使用该显式路径，否则使用 `exec.LookPath` 或等价机制从 PATH 查找。显式路径无效或 PATH 不存在 Codex 时返回清晰启动错误，不下载或静默回退。
- 启动前获取 Codex 版本；0.148.0 是已验证基线。其他版本在 stderr/结构化日志输出兼容性警告后继续尝试，stdout 不得出现非 ACP JSON-RPC 内容。
- Codex Adapter 使用解析到的 Codex 启动 `codex app-server` 子进程，只使用 stdin/stdout JSON-RPC；V1 不增加 HTTP、WebSocket 或 Unix Socket 传输。
- 启动后执行 app-server initialize，再允许 thread/turn 请求。
- stdout 只承载协议消息；stderr 进入结构化/关联日志，不能污染 JSON-RPC。
- 子进程退出、stdin/stdout 断开或 Adapter 关闭时，dispose 连接、取消活动请求并向 ACP 返回稳定错误；清理必须幂等。

## Session lifecycle

- new session 映射到 Codex thread/start，建立 ACP SessionID 与 Codex ThreadID 的内部关系。
- load/resume 使用固定上游基线的恢复流程，恢复必要历史与配置后才安装 session 状态。
- session open/close 使用 generation 或等价身份保护：迟到的 open 结果不能覆盖已关闭或重新打开的 session。
- 一个 session 的失败、关闭或恢复错误不得污染其他 session。

## Prompt and completion

- prompt 将 Text/Image/Resource 转换后的输入提交到 turn/start，并从对应 thread/turn 路由流式事件。
- 同一 session 可连续多轮；每个 prompt 只由自己 turn 的 completion 结束一次。
- completion 可能早于等待器建立；实现必须捕获并匹配 early completion。
- stale turn notification 不得完成当前 prompt；旧 turn 的等待器和状态在确定完成/取消后清理。

## Cancel and interrupt

- prompt 请求在 turn 启动前取消时不得留下后台 rival turn；若 turn 已活动，使用 Codex turn interrupt。
- interrupt 后等待对应 completion/cancelled 语义完成清理；重复取消幂等。
- session/进程结束会解除所有取消等待，不允许 goroutine 泄漏。

## Steering

- 每个 session 一个 FIFO steering queue，同一时间只处理一个 steering 请求。
- 有活动 turn 时发送 turn/steer；若上游返回“无活动 turn”竞态或 session 本来空闲，则按固定基线规则以 steering 内容启动新 turn。
- 并发 steering 不得启动 rival turn；请求在“注入活动 turn”或“新 turn 已被接受”后返回成功。
- 一个 steering 的解析、发送、启动或取消失败只拒绝该请求，队列继续处理后续请求。
- session 关闭时拒绝/清理未完成请求并移除空闲队列。

## Stale and failure protection

- thread、turn、session generation/identity 必须参与 completion、notification、approval 和 control 路由。
- early completion、stale completion、stale approval、close/open race 和 app-server 异常退出均有固定测试。

## Acceptance mapping

- A3：进程与 JSON-RPC 生命周期。
- A4-A7：session、prompt、cancel 和 steering。
- A13：early/stale/exit 竞态保护。
