# 原生 ACP 测试矩阵

日期：2026-09-08。自动测试分为标准 SDK 子进程 fixture 与锁定发布包真实进程链路；两者都不读取个人凭据。

| 场景 | 验证方式 |
| --- | --- |
| 初始化、缺失程序、完整环境 PATH | Go fixture 与依赖解析测试 |
| 模型、推理、旧 models/set_model | Go 子进程配置读写；发现不切模型 |
| 流式消息与 Cursor 交互 | Go fixture；问答、长计划正文、接受/拒绝、待办合并、task/image |
| MCP | Go fixture 三传输及 load 新请求头；Pi 真进程调用本地 STDIO / HTTP / SSE 服务 |
| Pi 权限 | 真进程 MCP 允许/拒绝、bash 允许/拒绝；拒绝无实际调用或写文件 |
| Pi 会话与配置 | 真进程 new A / new B / prompt A 隐式恢复、load A 更新凭据，参数特殊字符完整保留 |
| Kimi 隔离 | 配置不回写用户源、会话跨进程保留、无效配置错误不泄漏内容 |
| 取消与退出 | Go 阻塞 Prompt 取消、重复 Close、Unix 后代回收；Pi 真链路审批等待中取消无执行，退出无残留 fixture 进程 |
| Ally 业务 | 三种发现与执行、技能与指令、模型/推理、load、MCP 刷新和权限映射 |

```sh
go vet ./...
go test ./...
go test -race ./pkg/nativeacp ./pkg/pi ./pkg/kimi
```

真实 Pi 集成（Node >=22.19，安装目录与测试产物由调用者选择）：

```sh
npm install --prefix /tmp/acp-pi-deps @earendil-works/pi-coding-agent@0.84.1 pi-acp@0.0.33 pi-mcp-adapter@2.32.1
go build -o /tmp/acp-agent ./cmd/acp-agent
node scripts/pi-integration/run.mjs /tmp/acp-pi-deps /tmp/acp-agent
```

脚本在新临时目录隔离 HOME、Pi 配置和会话，运行本地假 OpenAI provider 及 MCP 服务，退出后打印结果与产物目录。验证了 11 次审批、6 次实际 MCP 调用、21 次本地模型请求；它验证真实 Pi 执行路径，不代表商业模型质量或真实账号登录通过。完整配置含测试凭据，所有值均为假数据。

真实 TypeScript Kimi 0.41.0 已在隔离目录验证 --version、initialize、无认证错误、使用本地假 provider 配置创建会话及模型/模式目录；没有发送商业模型请求。Python 版本的配置行为来自源码对照和 Go 文件隔离测试，尚未真实登录验收。

Cursor 官方 2026.09.02-c22c1a3 下载返回 HTTP 403，真实 CLI 测试未完成。macOS x64 是实测平台；Windows 批处理启动、符号链接及进程树回收尚未实测。Pi 上游 input/editor 未支持，auto/read-only 沙箱不在本接入支持范围。浏览器和桌面视觉验收未执行。
