package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

// recordingAuthServer 记录 account/read 和 account/login/start 的参数与调用顺序。
type recordingAuthServer struct {
	// events 保存认证边界调用顺序。
	events *[]string
	// account 是 account/read 响应。
	account protocol.GetAccountResponse
	// login 是 account/login/start 响应。
	login protocol.LoginAccountResponse
	// loginParams 保存收到的登录参数，测试结束前不得打印。
	loginParams []protocol.LoginAccountParams
	// err 是模拟 app-server 认证错误。
	err error
	// cancelled 保存收到的登录取消参数。
	cancelled []protocol.CancelLoginAccountParams
	// cancelContextErr 记录登录取消调用进入时的 context 状态。
	cancelContextErr error
	// cancelHasDeadline 记录独立清理 context 是否有界。
	cancelHasDeadline bool
}

// AccountRead 返回测试指定的当前账号。
func (s *recordingAuthServer) AccountRead(_ context.Context, _ protocol.GetAccountParams) (protocol.GetAccountResponse, error) {
	*s.events = append(*s.events, "read")
	return s.account, s.err
}

// AccountLogin 记录 typed 登录参数并返回测试指定响应。
func (s *recordingAuthServer) AccountLogin(_ context.Context, params protocol.LoginAccountParams) (protocol.LoginAccountResponse, error) {
	*s.events = append(*s.events, "login")
	s.loginParams = append(s.loginParams, params)
	return s.login, s.err
}

// AccountLoginCancel 记录 ChatGPT 登录取消请求。
func (s *recordingAuthServer) AccountLoginCancel(ctx context.Context, params protocol.CancelLoginAccountParams) (protocol.CancelLoginAccountResponse, error) {
	*s.events = append(*s.events, "cancel")
	s.cancelled = append(s.cancelled, params)
	s.cancelContextErr = ctx.Err()
	_, s.cancelHasDeadline = ctx.Deadline()
	return protocol.CancelLoginAccountResponse{Status: protocol.Canceled}, s.err
}

// recordingLoginSubscriber 创建可验证 subscribe-before-start 的订阅。
type recordingLoginSubscriber struct {
	// events 保存订阅顺序。
	events *[]string
	// completion 是 waiter 返回的 typed 完成通知。
	completion protocol.AccountLoginCompletedNotification
	// err 是订阅或等待错误。
	err error
	// waitErr 是订阅成功后的等待错误。
	waitErr error
}

// SubscribeLoginCompleted 先记录订阅，再返回一个独立 waiter。
func (s *recordingLoginSubscriber) SubscribeLoginCompleted() (loginCompletionSubscription, error) {
	*s.events = append(*s.events, "subscribe")
	if s.err != nil {
		return nil, s.err
	}
	return &recordingLoginSubscription{events: s.events, completion: s.completion, err: s.waitErr}, nil
}

// recordingLoginSubscription 是单次 account/login/completed 等待器。
type recordingLoginSubscription struct {
	// events 保存 wait/close 顺序。
	events *[]string
	// completion 是等待结果。
	completion protocol.AccountLoginCompletedNotification
	// err 是等待结果错误。
	err error
}

// Wait 返回预置的 typed 登录完成通知。
func (s *recordingLoginSubscription) Wait(_ context.Context) (protocol.AccountLoginCompletedNotification, error) {
	*s.events = append(*s.events, "wait")
	return s.completion, s.err
}

// Close 释放单次订阅。
func (s *recordingLoginSubscription) Close() {
	*s.events = append(*s.events, "close")
}

// recordingBrowserOpener 记录 ChatGPT 登录 URL。
type recordingBrowserOpener struct {
	// events 保存 browser 调用顺序。
	events *[]string
	// urls 保存要求打开的 URL。
	urls []string
}

// Open 记录 URL，不启动真实浏览器。
func (o *recordingBrowserOpener) Open(_ context.Context, url string) error {
	*o.events = append(*o.events, "open")
	o.urls = append(o.urls, url)
	return nil
}

// TestAuthMethodsAdvertiseOnlyV1Methods 验证 V1 仅公布 API Key 与 ChatGPT agent auth。
func TestAuthMethodsAdvertiseOnlyV1Methods(t *testing.T) {
	t.Parallel()

	methods := codexAuthMethods(true)
	if len(methods) != 2 || methods[0].Agent == nil || methods[0].Agent.Id != "api-key" || methods[1].Agent == nil || methods[1].Agent.Id != "chat-gpt" {
		t.Fatalf("auth methods = %#v", methods)
	}
	if methods[0].Agent.Meta["api-key"].(map[string]any)["provider"] != "openai" {
		t.Fatalf("api-key meta = %#v", methods[0].Agent.Meta)
	}
	withoutBrowser := codexAuthMethods(false)
	if len(withoutBrowser) != 1 || withoutBrowser[0].Agent.Id != "api-key" {
		t.Fatalf("browser disabled methods = %#v", withoutBrowser)
	}
}

// TestAuthenticatorUsesRequestThenCodexThenOpenAIKey 验证 API key 来源优先级和 subscribe-before-start。
func TestAuthenticatorUsesRequestThenCodexThenOpenAIKey(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct {
		// meta 是 ACP authenticate 请求携带的候选凭据。
		meta map[string]any
		// env 是测试注入的 Codex/OpenAI 环境凭据。
		env map[string]string
		// wantKey 是按优先级最终发送给 app-server 的凭据。
		wantKey string
	}{
		"request": {
			meta:    map[string]any{"api-key": map[string]any{"apiKey": "REQUEST_TOKEN"}},
			env:     map[string]string{"CODEX_API_KEY": "CODEX_TOKEN", "OPENAI_API_KEY": "OPENAI_TOKEN"},
			wantKey: "REQUEST_TOKEN",
		},
		"codex env": {
			env:     map[string]string{"CODEX_API_KEY": "CODEX_TOKEN", "OPENAI_API_KEY": "OPENAI_TOKEN"},
			wantKey: "CODEX_TOKEN",
		},
		"openai env": {
			env:     map[string]string{"OPENAI_API_KEY": "OPENAI_TOKEN"},
			wantKey: "OPENAI_TOKEN",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var events []string
			server := &recordingAuthServer{events: &events}
			subscriber := &recordingLoginSubscriber{
				events: &events, completion: protocol.AccountLoginCompletedNotification{Success: true},
			}
			auth := newAuthenticator(server, subscriber, &recordingBrowserOpener{events: &events}, func(key string) string {
				return fixture.env[key]
			}, slog.Default())
			err := auth.Authenticate(context.Background(), acp.AuthenticateRequest{MethodId: "api-key", Meta: fixture.meta})
			if err != nil {
				t.Fatalf("Authenticate 返回错误: %v", err)
			}
			if got := strings.Join(events, ","); got != "subscribe,login,wait,close" {
				t.Fatalf("API key 调用顺序 = %q", got)
			}
			if len(server.loginParams) != 1 || server.loginParams[0].Type != protocol.TypeAPIKey || server.loginParams[0].APIKey == nil || *server.loginParams[0].APIKey != fixture.wantKey {
				t.Fatalf("login params 未使用期望 key")
			}
		})
	}
}

// TestAuthenticatorChatGPTSubscribesBeforeLoginAndOpensBrowser 验证 ChatGPT 浏览器登录顺序。
func TestAuthenticatorChatGPTSubscribesBeforeLoginAndOpensBrowser(t *testing.T) {
	t.Parallel()

	var events []string
	authURL := "https://auth.example.test/login"
	server := &recordingAuthServer{
		events:  &events,
		account: protocol.GetAccountResponse{RequiresOpenaiAuth: true},
		login:   protocol.LoginAccountResponse{Type: protocol.TypeChatgpt, AuthURL: &authURL},
	}
	subscriber := &recordingLoginSubscriber{
		events: &events, completion: protocol.AccountLoginCompletedNotification{Success: true},
	}
	opener := &recordingBrowserOpener{events: &events}
	auth := newAuthenticator(server, subscriber, opener, func(string) string { return "" }, slog.Default())
	if err := auth.Authenticate(context.Background(), acp.AuthenticateRequest{MethodId: "chat-gpt"}); err != nil {
		t.Fatalf("Authenticate 返回错误: %v", err)
	}
	if got := strings.Join(events, ","); got != "read,subscribe,login,open,wait,close" {
		t.Fatalf("ChatGPT 调用顺序 = %q", got)
	}
	if len(opener.urls) != 1 || opener.urls[0] != authURL {
		t.Fatalf("opened URLs = %#v", opener.urls)
	}
}

// TestAuthenticatorSkipsChatGPTLoginForExistingAccount 验证已登录账号不创建订阅或新登录。
func TestAuthenticatorSkipsChatGPTLoginForExistingAccount(t *testing.T) {
	t.Parallel()

	var events []string
	server := &recordingAuthServer{
		events:  &events,
		account: protocol.GetAccountResponse{Account: &protocol.Account{Type: protocol.AccountTypeChatgpt}},
	}
	auth := newAuthenticator(
		server,
		&recordingLoginSubscriber{events: &events},
		&recordingBrowserOpener{events: &events},
		func(string) string { return "" },
		slog.Default(),
	)
	if err := auth.Authenticate(context.Background(), acp.AuthenticateRequest{MethodId: "chat-gpt"}); err != nil {
		t.Fatalf("Authenticate 返回错误: %v", err)
	}
	if got := strings.Join(events, ","); got != "read" {
		t.Fatalf("已登录调用顺序 = %q，期望仅 read", got)
	}
}

// TestAuthenticatorCancelsChatGPTLoginWhenWaitIsCancelled 验证等待取消会调用 app-server cancel。
func TestAuthenticatorCancelsChatGPTLoginWhenWaitIsCancelled(t *testing.T) {
	t.Parallel()

	var events []string
	authURL := "https://auth.example.test/login"
	loginID := "login-1"
	server := &recordingAuthServer{
		events:  &events,
		account: protocol.GetAccountResponse{RequiresOpenaiAuth: true},
		login:   protocol.LoginAccountResponse{Type: protocol.TypeChatgpt, AuthURL: &authURL, LoginID: &loginID},
	}
	subscriber := &recordingLoginSubscriber{events: &events, waitErr: context.Canceled}
	auth := newAuthenticator(
		server, subscriber, &recordingBrowserOpener{events: &events},
		func(string) string { return "" }, slog.Default(),
	)
	err := auth.Authenticate(context.Background(), acp.AuthenticateRequest{MethodId: "chat-gpt"})
	if err == nil {
		t.Fatal("取消的 ChatGPT 登录应返回错误")
	}
	if got := strings.Join(events, ","); got != "read,subscribe,login,open,wait,cancel,close" {
		t.Fatalf("取消调用顺序 = %q", got)
	}
	if len(server.cancelled) != 1 || server.cancelled[0].LoginID != loginID {
		t.Fatalf("cancel params = %#v", server.cancelled)
	}
	if server.cancelContextErr != nil || !server.cancelHasDeadline {
		t.Fatalf("cancel cleanup context: err=%v deadline=%v", server.cancelContextErr, server.cancelHasDeadline)
	}
}

// TestAuthenticatorNeverLeaksAPIKey 验证 app-server 错误、日志与返回错误都不暴露 secret。
func TestAuthenticatorNeverLeaksAPIKey(t *testing.T) {
	t.Parallel()

	const secret = "VERY_SECRET_TOKEN"
	var events []string
	server := &recordingAuthServer{events: &events, err: errors.New("upstream rejected " + secret)}
	subscriber := &recordingLoginSubscriber{
		events: &events, completion: protocol.AccountLoginCompletedNotification{Success: false, Error: acp.Ptr(secret)},
	}
	var logs bytes.Buffer
	auth := newAuthenticator(
		server, subscriber, &recordingBrowserOpener{events: &events},
		func(string) string { return "" }, slog.New(slog.NewTextHandler(&logs, nil)),
	)
	err := auth.Authenticate(context.Background(), acp.AuthenticateRequest{
		MethodId: "api-key",
		Meta:     map[string]any{"api-key": map[string]any{"apiKey": secret}},
	})
	if err == nil {
		t.Fatal("认证失败应返回错误")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatalf("secret 泄漏：error=%q logs=%q", err, logs.String())
	}
}
