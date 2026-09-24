package codex

import (
	"errors"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

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

// codexTurnRequestError 保留官方错误类别和诊断，不依赖错误文案推断重试语义。
func codexTurnRequestError(turnError *protocol.Error) *acp.RequestError {
	message := strings.TrimSpace(turnError.Message)
	if message == "" {
		message = "Codex turn failed"
	}
	data := map[string]any{}
	if info := turnError.CodexErrorInfo; info != nil {
		data["codexErrorInfo"] = info
		if kind := codexErrorKind(info); kind != "" {
			data["errorKind"] = kind
		}
	}
	if turnError.AdditionalDetails != nil && strings.TrimSpace(*turnError.AdditionalDetails) != "" {
		data["details"] = *turnError.AdditionalDetails
	}
	return &acp.RequestError{Code: -32603, Message: message, Data: data}
}

// codexErrorKind 只转换官方稳定类别，未知枚举留给宿主以未知错误展示。
func codexErrorKind(info *protocol.CodexErrorInfoUnion) string {
	if info.Enum != nil {
		switch string(*info.Enum) {
		case "unauthorized":
			return "authentication_failed"
		case "usageLimitExceeded", "sessionBudgetExceeded":
			return "quota_exhausted"
		case "serverOverloaded":
			return "overloaded"
		case "internalServerError":
			return "server_error"
		case "badRequest":
			return "invalid_request"
		case "contextWindowExceeded":
			return "context_window_exceeded"
		}
	}
	if value := info.CodexErrorInfo; value != nil {
		var status *int64
		switch {
		case value.HTTPConnectionFailed != nil:
			status = value.HTTPConnectionFailed.HTTPStatusCode
		case value.ResponseStreamConnectionFailed != nil:
			status = value.ResponseStreamConnectionFailed.HTTPStatusCode
		case value.ResponseStreamDisconnected != nil:
			status = value.ResponseStreamDisconnected.HTTPStatusCode
		case value.ResponseTooManyFailedAttempts != nil:
			status = value.ResponseTooManyFailedAttempts.HTTPStatusCode
		default:
			return ""
		}
		if status == nil {
			return "network_error"
		}
		switch *status {
		case 401:
			return "authentication_failed"
		case 402:
			return "billing_error"
		case 408, 504:
			return "timeout"
		case 429:
			return "rate_limit"
		}
		if *status >= 500 && *status <= 599 {
			return "server_error"
		}
	}
	return ""
}
