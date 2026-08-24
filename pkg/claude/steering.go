package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
	acp "github.com/coder/acp-go-sdk"
)

// steeringRequest 是 `_session/steering` 的已验证参数。
type steeringRequest struct {
	// SessionID 是目标 Session。
	SessionID string
	// Prompt 是待注入的用户内容。
	Prompt []acp.ContentBlock
	// PromptRequired 表示空闲时由客户端改用标准 prompt。
	PromptRequired bool
}

// steeringWireRequest 是扩展方法的 JSON 输入形状。
type steeringWireRequest struct {
	// SessionID 是目标 Session。
	SessionID string `json:"sessionId"`
	// Prompt 是待注入的用户内容。
	Prompt []acp.ContentBlock `json:"prompt"`
	// Meta 保存扩展选项。
	Meta *steeringMeta `json:"_meta,omitempty"`
}

// steeringMeta 保存 steering 扩展命名空间。
type steeringMeta struct {
	// Steering 是本扩展的行为配置。
	Steering *steeringBehavior `json:"steering,omitempty"`
}

// steeringBehavior 保存空闲 Session 的处理方式。
type steeringBehavior struct {
	// IdleBehavior 为空或 promptRequired。
	IdleBehavior string `json:"idleBehavior,omitempty"`
}

// parseSteeringRequest 严格校验 Session、prompt 和空闲行为。
func parseSteeringRequest(data json.RawMessage) (steeringRequest, error) {
	var wire steeringWireRequest
	if err := json.Unmarshal(data, &wire); err != nil {
		return steeringRequest{}, fmt.Errorf("decoding Claude steering request: %w", err)
	}
	if wire.SessionID == "" || len(wire.Prompt) == 0 {
		return steeringRequest{}, errors.New("Claude steering requires sessionId and a non-empty prompt")
	}
	idleBehavior := ""
	if wire.Meta != nil && wire.Meta.Steering != nil {
		idleBehavior = wire.Meta.Steering.IdleBehavior
	}
	if idleBehavior != "" && idleBehavior != "promptRequired" {
		return steeringRequest{}, fmt.Errorf("unsupported Claude steering idleBehavior %q", idleBehavior)
	}
	return steeringRequest{
		SessionID: wire.SessionID, Prompt: wire.Prompt, PromptRequired: idleBehavior == "promptRequired",
	}, nil
}

// steer 在活动 turn 中原子注入消息；空闲时按请求选择提示或 detached turn。
func (s *claudeSession) steer(ctx context.Context, message protocol.UserInputMessage, promptRequired bool) (map[string]any, error) {
	s.mu.Lock()
	if s.closed {
		err := s.fatalErr
		s.mu.Unlock()
		if err == nil {
			err = ErrClaudeSessionClosed
		}
		return nil, err
	}
	active := s.active
	if active != nil {
		// 活动身份检查、标记和写入保持在同一临界区，避免 turn 在决定注入后先行结束。
		active.mu.Lock()
		active.steeringIDs[message.UUID] = struct{}{}
		active.steered = true
		active.mu.Unlock()
		err := s.transport.Write(ctx, message)
		if err != nil {
			active.mu.Lock()
			delete(active.steeringIDs, message.UUID)
			active.mu.Unlock()
		}
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("injecting Claude steering message: %w", err)
		}
		return map[string]any{"outcome": "injected"}, nil
	}
	s.mu.Unlock()

	if promptRequired {
		return map[string]any{"outcome": "promptRequired", "reason": "noRunningTurn"}, nil
	}
	go func() {
		if _, err := s.prompt(s.ctx, message); err != nil && !errors.Is(err, context.Canceled) {
			s.agent.logger.Warn("Claude steering detached turn 失败", "session_id", s.id, "error", err)
		}
	}()
	return map[string]any{"outcome": "startedNewTurn"}, nil
}
