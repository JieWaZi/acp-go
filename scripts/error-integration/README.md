# 真实 CLI 故障注入

在已安装 Codex、Claude、Cursor、Kimi、Pi 的机器上运行真实程序，让模型或初始化请求连接本机 HTTP 故障服务。每个场景使用临时工作目录、独立配置和虚拟凭据，不复制个人凭据，不调用付费模型。测试通过 ACP 调用本仓库的适配器，不用手写 CLI 输出替换实际程序。CLI 自带遥测不属于本机模型 fixture 的验证范围。

```sh
go build -o /tmp/acp-error-probe ./scripts/error-integration
node scripts/error-integration/run.mjs /tmp/acp-error-probe
```

默认执行五种 CLI × 401、429、503，共 15 个场景。必须同时满足：本机服务收到实际请求、ACP 返回失败、助手正文为空。输出目录包含 `results.json`、每个场景的 `requests.json`、`acp.jsonl` 和进程诊断；HTTP 请求头、请求正文不写入采集报告。

```sh
# 单独验证 Kimi，以及包含 error 一词的正常回复不会误判。
ACP_ERROR_CLIS=kimi ACP_ERROR_SCENARIOS=auth,rate,unavailable,success \
  node scripts/error-integration/run.mjs /tmp/acp-error-probe
# 正常回复对照目前使用 OpenAI chat 流，支持 Kimi 和 Pi。
ACP_ERROR_CLIS=pi ACP_ERROR_SCENARIOS=success \
  node scripts/error-integration/run.mjs /tmp/acp-error-probe
# 同一原生会话先真实限流，再恢复接口并重发原输入。
ACP_ERROR_CLIS=kimi,pi ACP_ERROR_SCENARIOS=rate ACP_ERROR_RECOVER=1 \
  node scripts/error-integration/run.mjs /tmp/acp-error-probe
```

2026-09-23 实测版本：Codex 0.155.1、Claude 2.1.159、Cursor 2026.09.18-9a7762b、Kimi 2.0.2、Pi 0.85.1。

## 覆盖边界与发现

- Codex、Claude、Kimi、Pi：创建真实会话后，在模型 HTTP 请求中注入认证失败、限流和服务不可用。测试配置关闭或压缩 CLI 内部自动重试，只影响临时配置。
- Cursor：故障注入发生在 `session/new` 的认证交换和配置请求阶段；它只返回 `Failed to initialize session services`，丢失具体 HTTP 分类。只能确认初始化失败不会变成正常回复，不能将这三项算作模型回合覆盖，也不能凭 fixture 的已知 HTTP 状态给产品硬编码分类。
- Kimi 2.0.2 原生 ACP 在 429/503 耗尽后返回 `end_turn`，但官方主任务 Wire 记录为 `turn.ended reason=failed`。适配器补读本轮终态，并等待异步落盘；中间重试、子任务失败和旧轮次记录不提升为本轮错误。
- Kimi 当前配置项是 `max_attempts_per_step`，旧 `max_retries_per_step` 已失效；测试不能依赖旧键来控制运行时间。
- Kimi、Pi 的同会话失败后恢复均通过，下一轮正常回复不会被旧错误污染。
- 本测试验证真实程序与适配器；不等同于商业服务故障、账户套餐限制或页面视觉验收。Ally 另用采集到的协议错误回放 Runtime，验证稳定错误码。
