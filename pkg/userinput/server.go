package userinput

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverName = "acp_go_user_input"

// endpoint 将受管 MCP 问答绑定到唯一会话和当前执行，令牌不进入 Prompt。
type endpoint struct {
	// mutex 保护会话身份和当前执行上下文。
	mutex sync.Mutex
	// sid 是创建完成后绑定的 ACP 会话标识。
	sid acp.SessionId
	// turn 是当前执行上下文；空值拒绝会话外问答。
	turn context.Context
	// cancel 使取消和关闭立即结束等待。
	cancel context.CancelFunc
	// http 是只监听本机回环的受管 MCP 服务。
	http *http.Server
	// config 是仅该会话拥有的地址与随机凭据。
	config acp.McpServer
	// ready 在真实 CLI 成功读取工具目录时关闭。
	ready chan struct{}
	// readyOnce 保证工具发现通知幂等。
	readyOnce sync.Once
}

// newEndpoint 复用官方 MCP SDK 承载问答，不启动额外进程。
func newEndpoint(host Requester) (*endpoint, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		_ = listener.Close()
		return nil, err
	}
	token := "Bearer " + hex.EncodeToString(tokenBytes)
	ep := &endpoint{ready: make(chan struct{}), config: acp.McpServer{Http: &acp.McpServerHttpInline{Meta: map[string]any{"acp-go/user-input": true}, Type: "http", Name: serverName + "_" + hex.EncodeToString(tokenBytes[:8]), Url: "http://" + listener.Addr().String() + "/mcp", Headers: []acp.HttpHeader{{Name: "Authorization", Value: token}}}}}
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "AskUserQuestion", Description: "Ask the user a clarification or preference and wait for their answer. Available in every permission and working mode. Do not invent answers or treat permission approval as an answer. Supports text, single and multiple choices.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: acp.Ptr(false)}}, func(ctx context.Context, _ *mcp.CallToolRequest, input Questions) (*mcp.CallToolResult, Result, error) {
		ep.mutex.Lock()
		sid, turn := ep.sid, ep.turn
		ep.mutex.Unlock()
		if turn == nil || turn.Err() != nil {
			return nil, Result{}, errors.New("user input requires an active turn")
		}
		scoped, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(turn, cancel)
		defer stop()
		defer cancel()
		result, err := Ask(scoped, host, sid, input)
		if err != nil {
			return nil, Result{}, err
		}
		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: result}, result, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, request)
			if method == "tools/list" && err == nil {
				ep.readyOnce.Do(func() { close(ep.ready) })
			}
			return result, err
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	ep.http = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		handler.ServeHTTP(w, r)
	})}
	go func() { _ = ep.http.Serve(listener) }()
	return ep, nil
}

// stop 先取消交互再释放本机会话端口。
func (ep *endpoint) stop() { ep.endTurn(); _ = ep.http.Close() }

// endTurn 取消执行并使迟到的问答请求失效。
func (ep *endpoint) endTurn() {
	ep.mutex.Lock()
	defer ep.mutex.Unlock()
	if ep.cancel != nil {
		ep.cancel()
	}
	ep.turn = nil
	ep.cancel = nil
}

// cancelTurn 只取消当前问答，保留执行占位直到原生 Prompt 返回。
func (ep *endpoint) cancelTurn() {
	ep.mutex.Lock()
	defer ep.mutex.Unlock()
	if ep.cancel != nil {
		ep.cancel()
	}
}

// IsServer 识别由协议边界注入的受管问答配置，供适配器配置交互等待期限。
func IsServer(server acp.McpServer) bool {
	return server.Http != nil && server.Http.Meta["acp-go/user-input"] == true
}
