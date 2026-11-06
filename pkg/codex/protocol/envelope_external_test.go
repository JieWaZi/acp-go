package protocol_test

import (
	"testing"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

// TestEnvelopePromotesTypedParams 防止封闭 envelope 把 runtime 需要读取的强类型 Params 隐藏在包内。
func TestEnvelopePromotesTypedParams(t *testing.T) {
	idValue := int64(1)
	request := protocol.NewThreadStartRequest(
		protocol.RequestID{Integer: &idValue},
		protocol.ThreadStartParams{},
	)
	_ = request.Params

	raw := []byte(`{"method":"account/login/completed","params":{"success":true}}`)
	notification, err := protocol.DecodeServerNotification(raw)
	if err != nil {
		t.Fatalf("解析登录完成通知失败：%v", err)
	}
	completed, ok := notification.(*protocol.AccountLoginCompletedEnvelope)
	if !ok {
		t.Fatalf("通知类型 = %T，期望登录完成 envelope", notification)
	}
	if !completed.Params.Success {
		t.Fatal("包外无法读取登录完成的强类型 Params")
	}
}
