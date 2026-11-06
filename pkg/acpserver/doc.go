// Package acpserver 提供 Adapter 注册、选择和 ACP Agent 服务生命周期。
//
// Registry 延迟构造用户选中的 Adapter；Server 使用 acp-go-sdk 把选中的
// Agent 连接到调用方提供的输入输出流，并在连接结束时有界释放 Adapter 资源。
package acpserver
