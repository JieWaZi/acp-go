package claude

import (
	"errors"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

const acpResourceNotFoundCode = -32002

// claudeSessionNotFoundError 创建 ACP 标准的 Session 缺失错误。
func claudeSessionNotFoundError(sessionID string) *acp.RequestError {
	return &acp.RequestError{
		Code:    acpResourceNotFoundCode,
		Message: "Resource not found",
		Data: map[string]any{
			"resource":  "session",
			"sessionId": sessionID,
		},
	}
}

// mapClaudeSessionOpenError 把固定的恢复缺失形状提升为 ResourceNotFound。
func mapClaudeSessionOpenError(sessionID string, err error) error {
	missingConversation := strings.Contains(err.Error(), "No conversation found with session ID")
	if errors.Is(err, ErrClaudeSessionNotFound) || missingConversation {
		return claudeSessionNotFoundError(sessionID)
	}
	return err
}
