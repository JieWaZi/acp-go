# 原生 ACP 验证矩阵

默认测试只启动 Go helper fake CLI，使用真正的 `coder/acp-go-sdk` 进行双向协议往返；不读取账号、不联网、不调用模型、不计费。

| 场景 | OpenCode | Gemini CLI | Grok Build |
| --- | --- | --- | --- |
| argv、完整环境、调用者 slice 克隆、隔离变量 | fake CLI + real SDK | fake CLI + real SDK | fake CLI + real SDK |
| Adapter / CLI 版本分离 | fake CLI + real SDK | fake CLI + real SDK | fake CLI + real SDK |
| 原生模型/thinking 发现与 setter | 原生形状 fixture | 实际 legacy catalog 交集、模型 metadata、canonical thinking/默认恢复与失败原子替换 | 原生形状 fixture |
| thinking 内容独立转发 | fake CLI + real SDK | fake CLI + real SDK | fake CLI + real SDK |
| FIFO、取消代际、关闭与 process 回收 | 公共 nativeacp 行为与 race 覆盖 | 公共会话 FIFO/取消、独占关闭、崩溃及重载中关闭 fixture + race | 公共 nativeacp 行为与 race 覆盖 |
| Gemini 官方 CLI 实际生成与恢复 | 不适用 | opt-in 固定0.62.0 CLI，loopback 真 read_file/续轮LOW、同分钟历史恢复MEDIUM、default恢复原始HIGH/temperature | 不适用 |
| 审批 | 同 CLI reviewer + 真实 SDK 回调/审计；失败转人工 | auto allow_once/审计/失败回退、full-access yolo；官方零工具请求 opt-in | 原生 auto/yolo metadata fixture，未测真实分类器 |
| 统一即时 steering | 不支持 | 不支持 | fixture 证明 queued 回执映射，不证明模型消费 |
| 真实已安装 CLI 无账号冒烟 | 官方 1.18.34 reviewer loopback proof（opt-in） | 官方0.62生产 Go Adapter 多回合与零工具 reviewer loopback proof（opt-in） | 尚未执行 |
| 真实账号、真实模型、多回合静态前缀、工具与 MCP | 未测 | 未测 | 未测 |

默认验证：`go test ./pkg/opencode ./pkg/gemini ./pkg/grok ./pkg/nativeacp ./cmd/acp-agent`。官方实际 CLI 验证按 Gemini UPSTREAM 的 opt-in 命令。升级或接受新端点前仍需真实 CLI 多回合、取消、恢复、审批、MCP 与模型强度验证；freeze fixture 不替代原生执行证据。

Gemini 额外 fixture 覆盖：多会话隔离、空会话身份映射、全部标准文件/终端/permission/elicitation/vendor callback 映射、跨 Adapter 重启、原始 JSONL 失败回滚、Authenticate 重放、新凭据及原子刷新持久化、默认 profile 只读、显式空环境、符号链接拒绝、高优先级模型配置拒绝、restricted/false trust 人工回退。3.5 模型提升边界不公开不可保证的 thinking 选择。
