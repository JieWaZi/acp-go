package kimi

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/JieWaZi/acp-go/sdk"
	"github.com/pelletier/go-toml/v2"
)

// SetAgentConnection 保留标准宿主回调，用于补发 Kimi ACP 尚未提供的用量通知。
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

// ResumeSession 恢复当前进程中的原生会话，并重新绑定受管统计路径。
func (agent *Agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	result, err := agent.Agent.ResumeSession(ctx, request)
	if err == nil {
		agent.rememberWire(request.SessionId, request.Cwd)
	}
	return result, err
}

// rememberWire 对照 Kimi WorkDirMeta.sessions_dir，不扫描其他会话或用户数据。
func (agent *Agent) rememberWire(id acp.SessionId, cwd string) {
	if !agent.kimiCodeCLI.Load() || string(id) == "." || filepath.Base(string(id)) != string(id) || !filepath.IsAbs(cwd) {
		return
	}
	agent.wirePaths.Store(id, resolveWirePath(agent.codeDirectory, agent.directory, id, cwd))
}

// Prompt 保留原生 ACP 审批与交互，补齐当前轮 Wire 的真实终态与统计。
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
	if err == nil && ctx.Err() == nil && result.StopReason != acp.StopReasonCancelled {
		err = readWireFailure(ctx, path.(string), offset)
	}
	usage, update := readWireUsage(
		path.(string),
		offset,
		filepath.Join(agent.codeDirectory, "config.toml"),
	)
	if result.Usage == nil {
		result.Usage = usage
		if update != nil {
			if reported, ok := acpmeta.ReadTurnUsage(update.Meta); ok {
				result.Meta = acpmeta.WithTurnUsage(result.Meta, reported)
			}
		}
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
func readWireUsage(path string, offset int64, configPath string) (*acp.Usage, *acp.SessionUsageUpdate) {
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
	var reported *acpmeta.TurnUsage
	var update *acp.SessionUsageUpdate
	var contextTokens *int
	var model string
	for scanner.Scan() {
		var record wireUsageRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, nil
		}
		switch record.Type {
		case "usage.record":
			value := record.Usage
			if record.UsageScope == "turn" && value != nil {
				reported, usage = appendWireCounters(reported, value.Input, value.Output, value.CacheRead, value.CacheWrite)
				model = record.Model
			}
		case "token_counting.measured", "token_counting.turn_recorded":
			if record.Tokens != nil && *record.Tokens >= 0 {
				contextTokens = record.Tokens
			}
		}
		if record.Message.Type == "StatusUpdate" {
			status := record.Message.Payload
			value := status.TokenUsage
			if value != nil {
				reported, usage = appendWireCounters(reported, value.Input, value.Output, value.CacheRead, value.CacheWrite)
			}
			if status.ContextTokens != nil && status.MaxContextTokens != nil &&
				*status.ContextTokens >= 0 && *status.MaxContextTokens > 0 {
				update = &acp.SessionUsageUpdate{
					SessionUpdate: "usage_update",
					Used:          *status.ContextTokens,
					Size:          *status.MaxContextTokens,
				}
			}
		}
	}
	if scanner.Err() != nil {
		return nil, nil
	}
	if update == nil && contextTokens != nil {
		if size := readModelContextSize(configPath, model); size > 0 {
			update = &acp.SessionUsageUpdate{
				SessionUpdate: "usage_update",
				Used:          *contextTokens,
				Size:          size,
			}
		}
	}
	if usage != nil && reported != nil {
		reported.ContextKnown = update != nil
		reported.Model = model
		if update == nil {
			update = &acp.SessionUsageUpdate{}
		}
		update.Meta = acpmeta.WithTurnUsage(update.Meta, *reported)
	}
	return usage, update
}

// resolveKimiCodeDirectory 返回新版 Kimi Code 实际使用的用户数据目录。
func resolveKimiCodeDirectory(config Config) (string, error) {
	directory := nativeacp.EnvironmentValue(config.Environment, "KIMI_CODE_HOME")
	if directory == "" {
		home := nativeacp.EnvironmentValue(config.Environment, "HOME")
		if home == "" {
			home = nativeacp.EnvironmentValue(config.Environment, "USERPROFILE")
		}
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", err
			}
		}
		directory = filepath.Join(home, ".kimi-code")
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(config.WorkingDirectory, directory)
	}
	return filepath.Abs(directory)
}

// resolveWirePath 优先读取新版会话索引，找不到时回退旧版固定目录结构。
func resolveWirePath(codeDirectory, legacyDirectory string, id acp.SessionId, cwd string) string {
	if path := indexedWirePath(codeDirectory, id, cwd); path != "" {
		return path
	}
	digest := md5.Sum([]byte(cwd))
	return filepath.Join(
		legacyDirectory,
		"sessions",
		hex.EncodeToString(digest[:]),
		string(id),
		"wire.jsonl",
	)
}

// indexedWirePath 从 Kimi Code 自己的索引解析会话目录，不猜测 workspace 哈希算法。
func indexedWirePath(codeDirectory string, id acp.SessionId, cwd string) string {
	file, err := os.Open(filepath.Join(codeDirectory, "session_index.jsonl"))
	if err != nil {
		return ""
	}
	defer file.Close()
	root := filepath.Join(codeDirectory, "sessions")
	var result string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var record wireSessionIndexRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.SessionID != string(id) {
			continue
		}
		if record.WorkDir != "" && filepath.Clean(record.WorkDir) != filepath.Clean(cwd) {
			continue
		}
		directory, err := filepath.Abs(record.SessionDir)
		if err != nil || !pathWithin(root, directory) {
			continue
		}
		result = filepath.Join(directory, "agents", "main", "wire.jsonl")
	}
	return result
}

// pathWithin 判断候选路径是否仍位于 Kimi Code 的 sessions 根目录内。
func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// appendWireCounters 累计字段级已知消费，保留缺失与零；非法记录不清除先前快照。
func appendWireCounters(previous *acpmeta.TurnUsage, input, output, read, write *int) (*acpmeta.TurnUsage, *acp.Usage) {
	incoming := []*int{input, output, read, write}
	known := false
	for _, counter := range incoming {
		if counter != nil {
			if *counter < 0 {
				return previous, wirePromptUsage(previous)
			}
			known = true
		}
	}
	if !known {
		return previous, wirePromptUsage(previous)
	}
	result := acpmeta.TurnUsage{Source: "kimi.wire.usage", Coverage: "observed_requests"}
	if previous != nil {
		result = *previous
	}
	targets := []**int64{&result.InputTokens, &result.OutputTokens, &result.CacheReadTokens, &result.CacheWriteTokens}
	for index, target := range targets {
		value := incoming[index]
		if previous == nil {
			if value != nil {
				counter := int64(*value)
				*target = &counter
			}
		} else if *target != nil {
			if value == nil || int64(*value) > int64(math.MaxInt)-**target {
				*target = nil
			} else {
				counter := **target + int64(*value)
				*target = &counter
			}
		}
	}
	usage := wirePromptUsage(&result)
	if usage == nil {
		return previous, wirePromptUsage(previous)
	}
	return &result, usage
}

// wirePromptUsage 兼容标准必填数值字段，元数据负责保留对应分类是否已知。
func wirePromptUsage(reported *acpmeta.TurnUsage) *acp.Usage {
	if reported == nil {
		return nil
	}
	usage := &acp.Usage{}
	if reported.InputTokens != nil {
		usage.InputTokens = int(*reported.InputTokens)
	}
	if reported.OutputTokens != nil {
		usage.OutputTokens = int(*reported.OutputTokens)
	}
	if reported.CacheReadTokens != nil {
		value := int(*reported.CacheReadTokens)
		usage.CachedReadTokens = &value
	}
	if reported.CacheWriteTokens != nil {
		value := int(*reported.CacheWriteTokens)
		usage.CachedWriteTokens = &value
	}
	for _, counter := range []*int64{reported.InputTokens, reported.OutputTokens, reported.CacheReadTokens, reported.CacheWriteTokens} {
		if counter != nil {
			if *counter > int64(math.MaxInt-usage.TotalTokens) {
				return nil
			}
			usage.TotalTokens += int(*counter)
		}
	}
	return usage
}

// readModelContextSize 从本次 usage 对应模型的配置读取上下文窗口上限。
func readModelContextSize(configPath, model string) int {
	if configPath == "" || model == "" {
		return 0
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0
	}
	var config wireModelConfig
	if toml.Unmarshal(data, &config) != nil {
		return 0
	}
	return config.Models[model].MaxContextSize
}

// wireUsageRecord 保留 Kimi 官方 wire.jsonl 中与统计有关的最小结构。
type wireUsageRecord struct {
	// Type 是新版 Kimi Code 顶层事件类型。
	Type string `json:"type"`
	// Model 是 usage.record 对应的完整模型标识。
	Model string `json:"model"`
	// Usage 是新版单次模型调用的互斥 Token 分类。
	Usage *wireCurrentTokenUsage `json:"usage"`
	// UsageScope 区分当前轮统计与其他统计范围。
	UsageScope string `json:"usageScope"`
	// Tokens 是新版计数事件报告的当前上下文占用。
	Tokens *int `json:"tokens"`
	// Message 是持久化的官方 Wire 消息信封。
	Message wireUsageMessage `json:"message"`
}

// wireCurrentTokenUsage 对应新版 usage.record 的驼峰字段。
type wireCurrentTokenUsage struct {
	// Input 是扣除缓存读写后的输入 Token。
	Input *int `json:"inputOther"`
	// Output 是提供方报告的输出 Token。
	Output *int `json:"output"`
	// CacheRead 是缓存命中的输入 Token。
	CacheRead *int `json:"inputCacheRead"`
	// CacheWrite 是新创建缓存的输入 Token。
	CacheWrite *int `json:"inputCacheCreation"`
}

// wireSessionIndexRecord 是新版 session_index.jsonl 的会话定位信息。
type wireSessionIndexRecord struct {
	// SessionDir 是 Kimi Code 为会话分配的绝对目录。
	SessionDir string `json:"sessionDir"`
	// SessionID 是 ACP 返回的原生会话标识。
	SessionID string `json:"sessionId"`
	// WorkDir 是创建会话时的工作目录。
	WorkDir string `json:"workDir"`
}

// wireModelConfig 保存上下文展示所需的最小模型配置。
type wireModelConfig struct {
	// Models 按完整模型标识索引 Kimi Code 模型定义。
	Models map[string]wireModelDefinition `toml:"models"`
}

// wireModelDefinition 保存单个模型的上下文窗口上限。
type wireModelDefinition struct {
	// MaxContextSize 是模型允许的最大上下文 Token 数。
	MaxContextSize int `toml:"max_context_size"`
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
	Input *int `json:"input_other"`
	// Output 是提供方报告的输出 Token。
	Output *int `json:"output"`
	// CacheRead 是缓存命中的输入 Token。
	CacheRead *int `json:"input_cache_read"`
	// CacheWrite 是新创建缓存的输入 Token。
	CacheWrite *int `json:"input_cache_creation"`
}
