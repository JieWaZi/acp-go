package kimi

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// SetAgentConnection 保留标准宿主回调，用于补发 Python ACP 丢弃的用量通知。
func (agent *Agent) SetAgentConnection(host *acp.AgentSideConnection) {
	agent.host.Store(host)
	agent.Agent.SetAgentConnection(host)
}

// NewSession 记录官方会话的工作目录，统计只读取该受管会话的 wire 文件。
func (agent *Agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	result, err := agent.Agent.NewSession(ctx, request)
	if err == nil {
		agent.rememberWire(result.SessionId, request.Cwd)
	}
	return result, err
}

// LoadSession 恢复原生历史并重新绑定受管统计路径。
func (agent *Agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	result, err := agent.Agent.LoadSession(ctx, request)
	if err == nil {
		agent.rememberWire(request.SessionId, request.Cwd)
	}
	return result, err
}

// rememberWire 对照 Kimi WorkDirMeta.sessions_dir，不扫描其他会话或用户数据。
func (agent *Agent) rememberWire(id acp.SessionId, cwd string) {
	if !agent.pythonACP.Load() || string(id) == "." || filepath.Base(string(id)) != string(id) || !filepath.IsAbs(cwd) {
		return
	}
	digest := md5.Sum([]byte(cwd))
	agent.wirePaths.Store(id, filepath.Join(agent.directory, "sessions", hex.EncodeToString(digest[:]), string(id), "wire.jsonl"))
}

// Prompt 保留原生 ACP 审批与交互，只补齐当前轮官方 StatusUpdate 的统计。
func (agent *Agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	if _, active := agent.usagePrompts.LoadOrStore(request.SessionId, true); active {
		return acp.PromptResponse{}, acp.NewInvalidParams(nil)
	}
	defer agent.usagePrompts.Delete(request.SessionId)
	path, _ := agent.wirePaths.Load(request.SessionId)
	var offset int64
	if path != nil {
		if info, err := os.Stat(path.(string)); err == nil {
			offset = info.Size()
		}
	}
	result, err := agent.Agent.Prompt(ctx, request)
	if path == nil {
		return result, err
	}
	usage, update := readWireUsage(path.(string), offset)
	if result.Usage == nil {
		result.Usage = usage
	}
	if host := agent.host.Load(); host != nil && update != nil {
		// 提示取消后仍交付已经产生的统计；有界超时避免关闭时等待宿主。
		notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = host.SessionUpdate(notifyCtx, acp.SessionNotification{SessionId: request.SessionId, Update: acp.SessionUpdate{UsageUpdate: update}})
	}
	return result, err
}

// readWireUsage 对照官方 WireMessageRecord，只读取提示开始后的新增事件，不修改历史。
func readWireUsage(path string, offset int64) (*acp.Usage, *acp.SessionUsageUpdate) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.Size()-offset > 64*1024*1024 {
		return nil, nil
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return nil, nil
	}
	scanner := bufio.NewScanner(io.LimitReader(file, 64*1024*1024))
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	var usage *acp.Usage
	var update *acp.SessionUsageUpdate
	for scanner.Scan() {
		var record wireUsageRecord
		err := json.Unmarshal(scanner.Bytes(), &record)
		if record.Message.Type != "StatusUpdate" {
			continue
		}
		if err != nil {
			return nil, nil
		}
		status := record.Message.Payload
		if value := status.TokenUsage; value != nil && value.Input >= 0 && value.Output >= 0 && value.CacheRead >= 0 && value.CacheWrite >= 0 {
			if usage == nil {
				usage = &acp.Usage{CachedReadTokens: new(int), CachedWriteTokens: new(int)}
			}
			usage.InputTokens += value.Input
			usage.OutputTokens += value.Output
			*usage.CachedReadTokens += value.CacheRead
			*usage.CachedWriteTokens += value.CacheWrite
			usage.TotalTokens += value.Input + value.Output + value.CacheRead + value.CacheWrite
		}
		if status.ContextTokens != nil && status.MaxContextTokens != nil && *status.ContextTokens >= 0 && *status.MaxContextTokens > 0 {
			update = &acp.SessionUsageUpdate{SessionUpdate: "usage_update", Used: *status.ContextTokens, Size: *status.MaxContextTokens}
		}
	}
	if scanner.Err() != nil {
		return nil, nil
	}
	return usage, update
}

// wireUsageRecord 保留 Kimi 官方 wire.jsonl 中与统计有关的最小结构。
type wireUsageRecord struct {
	// Message 是持久化的官方 Wire 消息信封。
	Message wireUsageMessage `json:"message"`
}

// wireUsageMessage 区分状态事件与不应纳入统计的其他内容。
type wireUsageMessage struct {
	// Type 是官方消息类型，本适配只处理 StatusUpdate。
	Type string `json:"type"`
	// Payload 是状态快照与当前模型调用的用量。
	Payload wireUsageStatus `json:"payload"`
}

// wireUsageStatus 对应官方 StatusUpdate 的可选统计字段。
type wireUsageStatus struct {
	// TokenUsage 仅在模型调用完成时出现，不代表会话累计值。
	TokenUsage *wireTokenUsage `json:"token_usage"`
	// ContextTokens 是 CLI 报告的当前上下文占用。
	ContextTokens *int `json:"context_tokens"`
	// MaxContextTokens 是当前选中模型的窗口上限。
	MaxContextTokens *int `json:"max_context_tokens"`
}

// wireTokenUsage 对应 Kosong TokenUsage 的互斥输入与输出分类。
type wireTokenUsage struct {
	// Input 是扣除缓存读写后的输入 Token。
	Input int `json:"input_other"`
	// Output 是提供方报告的输出 Token。
	Output int `json:"output"`
	// CacheRead 是缓存命中的输入 Token。
	CacheRead int `json:"input_cache_read"`
	// CacheWrite 是新创建缓存的输入 Token。
	CacheWrite int `json:"input_cache_creation"`
}
