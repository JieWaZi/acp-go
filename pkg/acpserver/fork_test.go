package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// forkTestAgent 在真实协议边界记录可选分叉调用。
type forkTestAgent struct {
	// testAgent 提供基础会话协议和关闭行为。
	*testAgent
	// received 保存穿过问答包装层的原生请求。
	received chan acp.UnstableForkSessionRequest
}

// UnstableForkSession 返回独立身份，并原样记录工作区及不透明定位。
func (a *forkTestAgent) UnstableForkSession(_ context.Context, request acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	a.received <- request
	return acp.UnstableForkSessionResponse{SessionId: "child"}, nil
}

// TestUserInputWrapperForwardsForkOverProtocol 验证实际 NDJSON 调度能穿过问答包装层。
func TestUserInputWrapperForwardsForkOverProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	defer serverInput.Close()
	defer clientOutput.Close()
	defer clientInput.Close()
	defer serverOutput.Close()
	agent := &forkTestAgent{testAgent: newTestAgent(), received: make(chan acp.UnstableForkSessionRequest, 1)}
	server, err := NewWithUserInput(agent, serverInput, serverOutput)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	request := `{"jsonrpc":"2.0","id":1,"method":"session/fork","params":{"sessionId":"parent","cwd":"/child","_meta":{"forkPosition":"turn-1"}}}` + "\n"
	if _, err := io.WriteString(clientOutput, request); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(clientInput).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		// Result 保存标准协议的分叉结果。
		Result acp.UnstableForkSessionResponse `json:"result"`
		// Error 保存协议方法未转发等错误。
		Error *acp.RequestError `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result.SessionId != "child" {
		t.Fatalf("fork response = %s", line)
	}
	select {
	case got := <-agent.received:
		if got.SessionId != "parent" || got.Cwd != "/child" || got.Meta["forkPosition"] != "turn-1" {
			t.Fatalf("fork request = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
