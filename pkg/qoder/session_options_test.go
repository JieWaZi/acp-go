package qoder

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	acp "github.com/JieWaZi/acp-go/sdk"
)

// configBridge 仅替换原生 RPC 边界，保留真实配置请求和回执。
type configBridge struct {
	// request 保存适配器实际发给 CLI 的原生请求。
	request acp.SetSessionConfigOptionRequest
	// response 是 CLI 按原生身份返回的配置目录。
	response acp.SetSessionConfigOptionResponse
}

// SendRequest 校验真实 RPC 方法并返回原生回执。
func (b *configBridge) SendRequest(_ context.Context, method string, request any, response any) error {
	if method != "session/set_config_option" {
		return acp.NewMethodNotFound(method)
	}
	b.request = request.(acp.SetSessionConfigOptionRequest)
	*(response.(*acp.SetSessionConfigOptionResponse)) = b.response
	return nil
}

// TestReasoningCategoryIsNormalizedAtEveryBoundary 防止 Qoder 误报类别污染新建、恢复、动态更新及写入回执的标准目录。
func TestReasoningCategoryIsNormalizedAtEveryBoundary(t *testing.T) {
	for _, boundary := range []string{"session", "update", "receipt"} {
		t.Run(boundary, func(t *testing.T) {
			var options []acp.SessionConfigOption
			if err := json.Unmarshal([]byte(`[
				{"id":"reasoning_effort","name":"Reasoning","type":"select","category":"model","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]},
				{"id":"provider-model","name":"Model","type":"select","category":"model","currentValue":"qoder-model","options":[{"value":"qoder-model","name":"Qoder model"}]}
			]`), &options); err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(options)
			if err != nil {
				t.Fatal(err)
			}
			adapter := NewSessionAdapter()
			var got []acp.SessionConfigOption
			switch boundary {
			case "session":
				got, err = adapter.NormalizeSession(context.Background(), nil, "native", nativeacp.SessionSnapshot{ConfigOptions: options})
			case "update":
				got = adapter.NormalizeUpdate("native", options)
			case "receipt":
				bridge := &configBridge{response: acp.SetSessionConfigOptionResponse{ConfigOptions: options}}
				request := acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: "native", ConfigId: "reasoning_effort", Value: "high"}}
				var response acp.SetSessionConfigOptionResponse
				var handled bool
				response, handled, err = adapter.SetConfigOption(context.Background(), bridge, request)
				if !handled || !reflect.DeepEqual(bridge.request, request) {
					t.Fatalf("native configuration request changed: handled=%v request=%+v", handled, bridge.request)
				}
				got = response.ConfigOptions
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].Select == nil || got[0].Select.Category == nil || *got[0].Select.Category != acp.SessionConfigOptionCategoryThoughtLevel || got[0].Select.Id != "reasoning_effort" || got[0].Select.CurrentValue != "low" || got[1].Select == nil || got[1].Select.Category == nil || *got[1].Select.Category != acp.SessionConfigOptionCategoryModel || got[1].Select.Id != "provider-model" || got[1].Select.CurrentValue != "qoder-model" {
				t.Fatalf("Qoder catalog categories or native identities changed: %+v", got)
			}
			after, err := json.Marshal(options)
			if err != nil || string(after) != string(before) {
				t.Fatalf("native catalog mutated: before=%s after=%s error=%v", before, after, err)
			}
		})
	}
}
