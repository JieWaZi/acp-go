package codex

import (
	"errors"

	acp "github.com/coder/acp-go-sdk"
)

const acpResourceNotFoundCode = -32002

// codexSessionNotFoundError 创建 ACP 标准的 Session 缺失错误。
func codexSessionNotFoundError(sessionID string) *acp.RequestError {
	return &acp.RequestError{
		Code:    acpResourceNotFoundCode,
		Message: "Resource not found",
		Data: map[string]any{
			"resource":  "session",
			"sessionId": sessionID,
		},
	}
}

// mapCodexSessionOpenError 保留普通 app-server 错误，只提升稳定的资源缺失码。
func mapCodexSessionOpenError(sessionID string, err error) error {
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) && rpcErr.Code == acpResourceNotFoundCode {
		return codexSessionNotFoundError(sessionID)
	}
	return err
}
