package userinput

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authenticatedTransport 只给测试拥有的 MCP 端点附加会话凭据。
type authenticatedTransport struct {
	// token 是从受管配置读取的随机 Bearer 凭据。
	token string
}

// RoundTrip 为独立请求副本附加鉴权，避免修改原始请求。
func (t authenticatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", t.token)
	return http.DefaultTransport.RoundTrip(r)
}

// connectEndpoint 使用官方 MCP 客户端验证真实 HTTP 边界。
func connectEndpoint(t *testing.T, ep *endpoint) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "question-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ep.config.Http.Url, HTTPClient: &http.Client{Transport: authenticatedTransport{token: ep.config.Http.Headers[0].Value}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestQuestionMCPIsScopedAndCancelled 验证鉴权、会话外拒绝和取消向 HTTP 问答传播。
func TestQuestionMCPIsScopedAndCancelled(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	ep, err := newEndpoint(questionHost{callback: func(ctx context.Context, r acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
		if r.Form.Meta["sessionId"] != acp.SessionId("chat-a") {
			t.Error("cross-session question")
		}
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return acp.NewUnstableCreateElicitationResponseCancel(), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.stop()
	response, err := http.Get(ep.config.Http.Url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("MCP endpoint allowed missing credentials")
	}
	session := connectEndpoint(t, ep)
	call := func() (*mcp.CallToolResult, error) {
		return session.CallTool(context.Background(), &mcp.CallToolParams{Name: "AskUserQuestion", Arguments: Questions{Questions: []Question{{Question: "Continue?"}}}})
	}
	result, err := call()
	if err != nil || !result.IsError {
		t.Fatalf("inactive turn accepted: %#v %v", result, err)
	}
	ep.mutex.Lock()
	ep.sid = "chat-a"
	ep.turn, ep.cancel = context.WithCancel(context.Background())
	ep.mutex.Unlock()
	done := make(chan error, 1)
	go func() { _, err := call(); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP question never reached host")
	}
	ep.cancelTurn()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel not propagated")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP call still waiting")
	}
	if ep.turn == nil {
		t.Fatal("cancel released the execution slot before native completion")
	}
	result, err = call()
	if err != nil || !result.IsError {
		t.Fatalf("late question accepted: %#v %v", result, err)
	}
}

// TestQuestionMCPWaitsBeyondOneMinute 防止把普通工具的六十秒超时套用到用户输入。
func TestQuestionMCPWaitsBeyondOneMinute(t *testing.T) {
	if testing.Short() {
		t.Skip("long user interaction contract")
	}
	t.Parallel()
	ep, err := newEndpoint(questionHost{callback: func(ctx context.Context, _ acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
		timer := time.NewTimer(61 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return acp.NewUnstableCreateElicitationResponseCancel(), ctx.Err()
		}
		r := acp.NewUnstableCreateElicitationResponseAccept()
		r.Accept.Content = map[string]any{"question_0": "kept waiting"}
		return r, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer ep.stop()
	ep.sid = "patient-chat"
	ep.turn, ep.cancel = context.WithCancel(context.Background())
	result, err := connectEndpoint(t, ep).CallTool(context.Background(), &mcp.CallToolParams{Name: "AskUserQuestion", Arguments: Questions{Questions: []Question{{Question: "Long question?"}}}})
	if err != nil || result.IsError {
		t.Fatalf("long question failed: %#v %v", result, err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var answer Result
	if json.Unmarshal(data, &answer) != nil || answer.Action != "accept" || answer.Answers["Long question?"] != "kept waiting" {
		t.Fatalf("lost delayed answer: %s", data)
	}
}
