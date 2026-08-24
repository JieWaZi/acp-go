// Package codex 把 ACP Agent 生命周期适配到用户预装的 Codex CLI app-server。
//
// 调用方通过 Config 提供日志与可执行文件选择，并使用 NewAgent 构造可交给
// acp-go-sdk 连接层的 Agent。协议 DTO 与 wire envelope 位于 protocol 子包。
package codex
