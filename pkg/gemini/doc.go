// Package gemini 通过现有 ACP SDK 为每个会话管理独立的官方 Gemini CLI 进程。
// 模型来自原生目录，思考档位来自精确能力交集；空闲修改通过真实历史恢复后原子替换。
// 公共会话身份保持稳定，关闭释放对应子进程；Config.Environment 非 nil 时完全替换环境。
package gemini
