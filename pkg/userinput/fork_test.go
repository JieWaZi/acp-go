package userinput

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// forkAgentProbe 记录包装层提交给原生适配器的参数。
type forkAgentProbe struct {
	// Agent 提供未在测试中调用的标准协议签名。
	acp.Agent
	// request 保存最近一次分叉输入。
	request acp.UnstableForkSessionRequest
}

// UnstableForkSession 模拟创建独立会话，不处理模型输入。
func (a *forkAgentProbe) UnstableForkSession(_ context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	a.request = request
	return acp.UnstableForkSessionResponse{SessionId: "child"}, nil
}

// TestForkAllocatesIndependentUserInputEndpoint 验证父子会话各自持有问答端点。
func TestForkAllocatesIndependentUserInputEndpoint(t *testing.T) {
	native := &forkAgentProbe{}
	agent := Wrap(native)
	agent.enabled = true
	defer agent.Close(context.Background())
	_, parent, err := agent.prepare(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.attach(parent, "parent", nil); err != nil {
		t.Fatal(err)
	}
	response, err := agent.UnstableForkSession(context.Background(), acp.UnstableForkSessionRequest{SessionId: "parent", Cwd: "/child", Meta: map[string]any{"forkPosition": "native-turn"}})
	if err != nil {
		t.Fatal(err)
	}
	child := agent.sessions[response.SessionId]
	if child == nil || child == parent || agent.sessions["parent"] != parent {
		t.Fatal("分叉必须保留父端点并绑定独立子端点")
	}
	if len(native.request.McpServers) != 1 || native.request.McpServers[0].Http.Url != child.config.Http.Url || child.config.Http.Url == parent.config.Http.Url {
		t.Fatal("原生分叉未挂载子端点")
	}
	if native.request.Meta["forkPosition"] != "native-turn" {
		t.Fatal("原生定位丢失")
	}
	agent.remove("child")
	if agent.sessions["parent"] != parent {
		t.Fatal("关闭子会话影响父端点")
	}
}
