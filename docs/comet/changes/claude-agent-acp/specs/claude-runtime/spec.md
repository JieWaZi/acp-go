# Claude runtime complete target specification

## Executable and process lifecycle

- Claude CLI 由用户预装。`CLAUDE_CODE_EXECUTABLE` 非空时只使用该显式路径；为空时从 PATH 查找 `claude`。显式路径无效或 PATH 无可执行文件时返回清晰错误，不下载、不捆绑、不启动 Node。
- 构造时以有界命令获取版本并写 stderr 诊断；没有已确认的 CLI semver 兼容范围时不伪造硬性版本判定，协议兼容基线由 Agent SDK 0.3.232 fixtures 表达。
- 每个 ACP Session 启动独立 Claude CLI，基础参数是 `--output-format stream-json --verbose --input-format stream-json`；权限回调使用 `--permission-prompt-tool stdio`，其余参数由 Session 配置、MCP 和 additional directories 决定。
- stdout 只进入 Claude JSONL reader，stderr 进入有界/脱敏 logger，stdin 写入由 transport 串行化；非零退出、信号退出、写失败和 EOF 保留稳定错误类别与有限 stderr tail。
- Session close 先解除 prompt/control/permission 等待，再结束 stdin、温和终止进程并在有界期限后强制终止；重复 close 与 Adapter Close 幂等。

## Session lifecycle

- new session 使用 cwd、additional directories、client MCP 与初始配置构造进程，完成 control initialize/首个 init 消息握手后才把 Session 发布到 store。
- ACP SessionID 使用 Claude 返回的持久 Session ID；构造失败、关闭竞态或迟到 init 不得安装残缺 Session。
- resume 使用 `--resume=<session-id>` 恢复并返回配置能力，不向客户端回放既有消息。
- load 使用相同恢复身份并按 v0.70.0 语义回放可表达历史；过滤 local-command marker、system/internal-only 消息和 V1 不支持的扩展，保持 user/assistant/tool 顺序。
- 同一 SessionID 在 cwd/MCP/additional directories 等定义参数改变时关闭旧进程并按新参数重建，不能复用错误进程；失败时不污染其他 Session。
- close session 仅作用于活动内存 Session，不删除磁盘 transcript；不存在或已经关闭的 Session 返回稳定错误/幂等语义按 ACP 方法契约处理。

## Prompt lifecycle

- 每个 Session 维护一个长期 consumer 和 FIFO turn queue；prompt 创建唯一 user-message UUID，先安装 turn waiter 再写入输入，避免 echo/result 早到竞态。
- consumer 按 user echo 激活对应 turn，按 stream/assistant/result/idle 结束；每个 prompt 恰好 settle 一次，多轮顺序和 usage 不串线。
- stream 在无 result、异常结束或进程死亡时解除所有 turn；Session 进入不可恢复状态后，后续 prompt 返回“新建 Session”类型的清晰错误而不写入死 pipe。
- queue、partial content、tool cache 和迟到/orphan 记账全部有界；旧 UUID/result/idle 不得激活或完成后续 turn。

## Cancel and steering

- cancel 标记当前 generation，发送 interrupt control request，立即取消尚未开始的排队 turn，并在有界 grace 后解除卡住的活动 prompt；重复 cancel 幂等。
- cancel 后的 echo/result/idle 只用于清理对应 orphan，不得完成新 turn；cancel 与 close/process exit 的先后顺序不造成永久等待。
- `_session/steering` 复用 acp-go-sdk extension handler。活动 turn 中将带唯一 UUID 和 `priority: "now"` 的用户消息注入同一输入流，并让原 prompt 在正确 idle 后完成。
- Session 空闲时按 v0.70.0 默认行为启动 detached turn 并返回 `startedNewTurn`；支持 upstream `promptRequired` idle behavior 时只返回提示，不偷写输入。
- steering、prompt、cancel、close 在一个 Session 的状态锁/fence 下判定，不能产生 rival turn；一个 steering 失败不破坏 Session 后续请求。

## Acceptance mapping

- A3：CLI 解析与版本诊断。
- A4-A5：进程/transport 生命周期和错误。
- A6-A7：new/load/resume Session。
- A8-A9：prompt、steering、cancel。
- A16：关闭/退出竞态和资源回收。
