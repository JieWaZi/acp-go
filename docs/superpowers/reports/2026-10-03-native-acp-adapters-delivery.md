# OpenCode、Gemini CLI、Grok Build 交付记录

三个公共适配器与 Ally 的运行时、账号、模型/thinking、权限、问答、指令/技能、MCP 和恢复路径已实现。最终收尾按用户要求由主代理直接处理，没有新增子代理。

## 代码与验收范围

acp-go 功能基线为 `f708981ab87259df64f54a589907f52d17b081d5`，本地分支 `codex/native-acp-adapters`；Ally 同名本地分支位于独立 checkout，集成提交为 `24acdb872b7663ba7965a620c43df2d6ac0ac2ce`。没有推送、发布或合并，原始 acp-go 的用户 `.gitignore` 修改保留，原始 Ally 只用于无改动对照测试。

共同产品能力包括真实模型与模型专属 thinking 选项、独立思考内容流、三级权限、当前执行的审批与问答、原生恢复，以及账号 ID/修订隔离。项目账号沿用已有聊天选择和冻结工作范围，不增加独立项目默认账号绑定。

未声明的即时 steering、分叉或严格只读保持不可用。Grok 插话只确认 queued。未知菜单与版本保持未知，不从模型名猜能力或从计费 Token 推断上下文占用。OpenCode/Gemini 自动审批使用无工具审查；Grok 使用官方风险分类器，遵守管理员规则与 auto 停用开关。

## 直接复核与修复

前期 Task1/Task2 及 Gemini 历史/恢复、工具证据修复已接受独立审查。最终由主代理读取 Ally 注册、公共构造器、实际选中模型目录、执行验证、受管配置、端点认证、账号修订、原始 ID 映射、前端表单和文档；结合回归断言与保存的原始验证日志复核，不将这次直接自审称为独立审查。

- M1 已修复：Gemini 默认 overlay 测试的无效循环改成完整 modelConfigs 快照比较，原始档位覆盖测试另有真实断言。
- M3 已修复：三个 UPSTREAM 的 acpx 链接改为 `openclaw/acpx`。
- Gemini 测试夹具 race 超时已复现并修复：多次 owned-child 替换等待 race 子进程退出，原来共用 5 秒时限；只增加该测试时限到 30 秒，没有修改生产超时。
- M2 保留：SDK 关闭 INFO 日志是清理诊断。SDK 构造立即启动 goroutine，SetLogger 没有同步保护；为消除噪声而在启动后更换 logger 会引入风险，未采用。
- Ally 文档修正 Grok 原生 auto 与另两种无工具审查的差异，并提供可执行的临时 workspace 和正式升级依赖步骤。
- 直接复核发现并修复 Gemini 受管信任判断：用真实路径和 filepath.Rel 判断祖先，不再遗漏根目录或软链接父目录的显式拒绝。两条新增测试先失败，再通过；日志 `/tmp/ally-direct-trust-red.log`、`/tmp/ally-direct-trust-green.log`。

## 验证证据

- acp-go：`go test ./... && go vet ./... && go build ./...` 退出 0，最终日志 `/tmp/acp-direct-final-go.log`。
- acp-go：`go test -race ./pkg/opencode ./pkg/gemini -count=1` 退出 0，最终日志 `/tmp/acp-direct-adapters-race-green.log`；OpenCode 22.686 秒，Gemini 144.086 秒。此前 nativeacp 全量 race 已通过，最终只修改测试与文档。
- Ally 修改后后端 `go vet ./... && go test ./...` 已退出 0，日志 `/tmp/ally-task3-backend-final.log`。全程使用仓库外临时 Go workspace，实际使用新 ACP 源码。
- Ally 新增账号/运行时/API/执行回归包通过；前端新增兼容性与账号表单回归通过。API 生成由项目命令执行。
- Ally 直接对照：新增分支两个聊天测试文件 3 失败、56 通过；未修改原始 checkout 聚焦同类测试 5 失败、1 通过、53 跳过。原始 checkout 同样复现团队 15 秒超时和消息渲染断言失败；日志 `/tmp/ally-direct-focused-vitest.log`、`/tmp/ally-direct-baseline-focused.log`。没有放宽这些无关测试的时限或修改其生产逻辑。
- 最终 Ally `GOWORK=/tmp/ally-native-adapters.go.work pnpm check` 退出 0，日志 `/tmp/ally-direct-final-check.log`：API 一致性、类型检查、lint、UI、格式全部通过；Vitest 196 文件/1056 用例全部通过，耗时 795.46 秒；Vite 构建、bundle 检查、Electron 构建和 52 用例通过；后端 vet 和全量 Go 测试通过，覆盖最后的信任边界修复。此前在基线复现的前端失败没有在最终运行中出现。

官方 CLI 证据单独记录：固定 Gemini 0.62 实际 CLI 在本地 loopback 中通过模型/thinking 目录、三次进程重建跨权限恢复、第三次请求保留前两次真实历史，真实请求使用 LOW。实际 HTTP thinking 查询通过，耗时 30.715 秒；SDK 三轮证明耗时 143.08 秒。ACP 另有官方 Gemini 真工具/思考切换/默认恢复和 OpenCode reviewer 零工具证明。没有付费推理、真实 OAuth、真实 Grok 可执行文件、外部 MCP、Windows 或可视化验收。

最终直接自审没有未处理的重要代码问题。开发与联合工作区验收完成；独立发布仍需下面的依赖升级及真实外部环境验收。

## 依赖发布与实际限制

Ally 当前 go.mod 的已发布 ACP 版本不包含新公共包，单独 checkout 不能靠旧依赖构建新绑定。必须先发布可获取的上述 ACP 功能提交，再在 Ally server 运行 `go get github.com/JieWaZi/acp-go@f708981ab87259df64f54a589907f52d17b081d5`，提交实际 go.mod/go.sum 并重新验证。本次不提交绝对 replace 或本机 go.work。可复现本地步骤见 Ally `docs/native-runtime-accounts-cn.md`。

Gemini 冷启动能力查询约 23–31 秒，定向验证每次启动隔离 CLI，可能延迟保存/发送；未添加能力缓存。Grok 受管账号只开放已核验认证路径的 Responses/Chat Completions，不宣称 Messages。真实服务商和外部安装环境仍需发布前实测。

## 实施取舍及代价

以下保留全部原始控制器取舍；后续 owned-child 的真实 CloseSession 实现已经取代早期 Gemini close 不支持的边界。

Ruling: 以原生 worktree 工具选择的最新主线 48b2b12 为基线，并复用 pkg/nativeacp；新增生命周期语义显式 opt-in，不重复 transport — Ally 当前已有五种运行时，最新主线已有对应能力 — 若基线选择不符用户预期，需要把新增包回移到旧提交，不能改主工作区的用户文件。

Ruling: FIFO 并发通过公共 Go Agent.Prompt 验证，标准 ACP E2E 验证串行请求与回调 — coder SDK 同 session 新协议 prompt 会主动取消旧请求 ctx，现有适配器也受其约束；不修改 SDK — 若需要协议端无取消并发，须另改 ACP 外层调度。

Ruling: OpenCode/Gemini无官方会中steering保持unsupported，Grok仅承诺queued；Gemini可用silent load实现真实resume，原生close真实MethodNotFound，Ally不得把它作为具备close的热驻留会话 — 固定上游没有这些方法且不允许取消重开冒充steering — 若用户要求所有CLI会中注入完全一致，需要独立最小上游扩展而不是改声明。

Ruling: 新增三种默认/自动/完全访问产品权限仍是验收范围；Geminiauto_edit/yolo不映射auto，必须用真实nativeauto或安全无工具review，缺失证据/deny/error回人工并发出审计元数据 — 用户要求新能力与Codex/Claude一致，不能自行删auto — 若不存在安全官方执行方式，先向控制器报告具体阻碍。

Ruling: Gemini 账号默认配置只读，兼容配置写 adapter 私有目录；新增 StateDirectory 控制持久状态，显式 GEMINI_CLI_HOME 管理目录允许保存其会话状态 — 用户要求账号隔离，官方系统 overlay 会因所有权校验忽略 — 若目录语义不符合宿主预期，需调整持久状态布局和迁移，而不能改用户真实目录。

Ruling: Gemini model setter 接受非空官方 canonical model ID，并传给官方 setter；thinking 只支持固定官方规则与实际能力目录交集 — 原生 setter 无白名单且账户可能提供自定义模型，不能伪造 availableModels — 若不符合上游授权模型，实际调用应返回上游错误，需宿主展示该错误。

Ruling: Task2 分两阶段，先完成通用/OpenCode/Grok，再由专门 Gemini 实现者完成 thinking；Gemini 每个会话独立原生进程，canonical 配置固定，闲置时配置变化通过官方 load 准备新进程后成功交换；忙碌时拒绝设置 — 官方别名工具续轮会丢失 effort，BeforeModel 不支持 thinking，load 读取缓存启动设置 — 若此生命周期语义不符宿主需要，需独立上游 config setter 扩展；不允许取消重开冒充 steering。

Ruling: Gemini 专有会话子进程使 adapter 可以真实关闭单个会话并声明 close，取代之前薄代理 close unsupported 边界；实现须释放进程/等待者且保留可恢复历史 — 承载实际资源的新所有权允许补齐用户要求的生命周期能力 — 若关闭无法安全解除回调或恢复历史，必须收回 close 声明并修复生命周期，而不能只返回成功。

Ruling: 项目级账号配置沿用 Ally 现有账号选择、聊天受管工作区和冻结执行快照，不新增项目默认账号领域 — 用户要求新增CLI与已有Codex/Claude一致，CONTEXT明确当前不建立项目默认账号 — 若用户指的是新的项目默认账号绑定，需要另增领域/API/UI及迁移；本次仍须完整覆盖新增CLI的现有项目执行配置。

## 过程资料归档

全部九项取舍及判断错误时的代价已逐字保留于上文。当前计划的临时审查/实现资料另存于 `/tmp/native-acp-adapters-sdd-20261003.tar.gz`（仅当前用户可读），仅清理本计划 scratch；两个代码工作区和上游源码缓存仍保留。
