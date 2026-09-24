package codex

import (
	"encoding/json"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	"testing"
)

// TestCodexProviderErrorContract 验证稳定类别、HTTP 状态与未知错误不依赖消息文案。
func TestCodexProviderErrorContract(t *testing.T) {
	cases := []struct {
		// wire 保存官方错误信息的原始 JSON。
		wire string
		// kind 是宿主可使用的开放错误类别；空值表示未知。
		kind string
	}{
		{`"unauthorized"`, "authentication_failed"},
		{`"usageLimitExceeded"`, "quota_exhausted"},
		{`"sessionBudgetExceeded"`, "quota_exhausted"},
		{`"serverOverloaded"`, "overloaded"},
		{`"internalServerError"`, "server_error"},
		{`"badRequest"`, "invalid_request"},
		{`"contextWindowExceeded"`, "context_window_exceeded"},
		{`"other"`, ""},
		{`"futureError"`, ""},
		{`{"httpConnectionFailed":{"httpStatusCode":401}}`, "authentication_failed"},
		{`{"httpConnectionFailed":{"httpStatusCode":402}}`, "billing_error"},
		{`{"responseStreamConnectionFailed":{"httpStatusCode":429}}`, "rate_limit"},
		{`{"responseStreamDisconnected":{"httpStatusCode":503}}`, "server_error"},
		{`{"responseTooManyFailedAttempts":{"httpStatusCode":504}}`, "timeout"},
		{`{"httpConnectionFailed":{"httpStatusCode":403}}`, ""},
		{`{"responseStreamDisconnected":{}}`, "network_error"},
	}
	for _, test := range cases {
		t.Run(test.wire, func(t *testing.T) {
			var info protocol.CodexErrorInfoUnion
			if err := json.Unmarshal([]byte(test.wire), &info); err != nil {
				t.Fatal(err)
			}
			got := codexTurnRequestError(&protocol.Error{Message: "provider rejected request", CodexErrorInfo: &info})
			data, _ := got.Data.(map[string]any)
			kind, _ := data["errorKind"].(string)
			if got.Message != "provider rejected request" || kind != test.kind || data["codexErrorInfo"] == nil {
				t.Fatalf("类别或诊断丢失：%+v", got)
			}
		})
	}
	empty := codexTurnRequestError(&protocol.Error{})
	if empty.Message == "" {
		t.Fatal("无诊断错误没有兜底说明")
	}
}
