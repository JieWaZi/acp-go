package userinput

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverName = "acp_go_user_input"

// basicConfigDir 解析本机用户配置目录，测试使用私有临时目录。
var basicConfigDir = os.UserConfigDir

// basicAuthorization 在本机用户配置中保持稳定，避免会话或进程轮换改变 MCP 鉴权配置。
var basicAuthorization = sync.OnceValues(func() (string, error) {
	secret, err := loadBasicSecret()
	if err != nil {
		return "", err
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("acp-go:"+secret)), nil
})

// loadBasicSecret 只从当前用户可读的私有文件读取或生成固定凭据。
func loadBasicSecret() (string, error) {
	root, err := basicConfigDir()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(root, "acp-go")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "user-input-basic-secret")
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, ".user-input-basic-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(hex.EncodeToString(secret)); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("user input Basic credential file must be private: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if len(value) != 64 {
		return "", errors.New("invalid user input Basic credential")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", err
	}
	return value, nil
}

// activeEndpoints 只记录本进程创建的端点；ACP 请求中的元数据不能证明受管身份。
var activeEndpoints sync.Map

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
	// config 是仅该会话拥有的地址与本机用户稳定的 Basic 凭据。
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
	authorization, err := basicAuthorization()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	pathSecret := make([]byte, 16)
	if _, err := rand.Read(pathSecret); err != nil {
		_ = listener.Close()
		return nil, err
	}
	endpointPath := "/mcp/" + hex.EncodeToString(pathSecret)
	ep := &endpoint{ready: make(chan struct{}), config: acp.McpServer{Http: &acp.McpServerHttpInline{Meta: map[string]any{"acp-go/user-input": true}, Type: "http", Name: serverName, Url: "http://" + listener.Addr().String() + endpointPath, Headers: []acp.HttpHeader{{Name: "Authorization", Value: authorization}}}}}
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
		if r.URL.Path != endpointPath || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(authorization)) != 1 {
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
	activeEndpoints.Store(ep.config.Http.Url, ep.config.Http)
	go func() { _ = ep.http.Serve(listener) }()
	return ep, nil
}

// stop 先取消交互再释放本机会话端口。
func (ep *endpoint) stop() {
	activeEndpoints.Delete(ep.config.Http.Url)
	ep.endTurn()
	_ = ep.http.Close()
}

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

// IsServer 只识别本进程实际创建的端点，不能信任调用方可伪造的 MCP 元数据。
func IsServer(server acp.McpServer) bool {
	if server.Http == nil {
		return false
	}
	owned, ok := activeEndpoints.Load(server.Http.Url)
	return ok && owned == server.Http
}
