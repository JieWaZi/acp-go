package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"acp-go/agents/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

var (
	// errAuthenticationFailed 是不包含上游详情或凭据的认证失败哨兵错误。
	errAuthenticationFailed = errors.New("authentication failed")
	// errAPIKeyMissing 说明两个受支持环境变量都未提供 API key。
	errAPIKeyMissing = errors.New("CODEX_API_KEY or OPENAI_API_KEY is not set")
)

// authServer 是认证组件消费的最小 Codex app-server 能力。
type authServer interface {
	// AccountRead 读取当前账号并可要求 app-server 刷新 token。
	AccountRead(context.Context, protocol.GetAccountParams) (protocol.GetAccountResponse, error)
	// AccountLogin 启动 API key 或 ChatGPT 登录。
	AccountLogin(context.Context, protocol.LoginAccountParams) (protocol.LoginAccountResponse, error)
	// AccountLoginCancel 取消尚未完成的 ChatGPT 登录。
	AccountLoginCancel(context.Context, protocol.CancelLoginAccountParams) (protocol.CancelLoginAccountResponse, error)
}

// loginCompletionSubscription 表示在 account/login/start 前安装的单次完成订阅。
type loginCompletionSubscription interface {
	// Wait 等待 typed account/login/completed 通知。
	Wait(context.Context) (protocol.AccountLoginCompletedNotification, error)
	// Close 释放订阅，避免登录失败后遗留监听器。
	Close()
}

// loginCompletionSubscriber 创建单次 account/login/completed 订阅。
type loginCompletionSubscriber interface {
	// SubscribeLoginCompleted 必须在 AccountLogin 前调用。
	SubscribeLoginCompleted() (loginCompletionSubscription, error)
}

// browserOpener 是 ChatGPT 登录组件消费的最小浏览器能力。
type browserOpener interface {
	// Open 打开 app-server 返回的认证 URL。
	Open(context.Context, string) error
}

// environmentLookup 读取一个环境变量，便于测试凭据优先级而不修改进程全局状态。
type environmentLookup func(string) string

// authenticator 仅实现 V1 API Key 与 ChatGPT 浏览器认证。
type authenticator struct {
	// server 是 typed app-server 认证 API。
	server authServer
	// subscriber 在登录启动前安装完成通知监听器。
	subscriber loginCompletionSubscriber
	// browser 打开 ChatGPT 登录页面。
	browser browserOpener
	// getenv 读取 CODEX_API_KEY 与 OPENAI_API_KEY。
	getenv environmentLookup
	// logger 只记录无详情的边界错误，禁止记录 secret 或 payload。
	logger *slog.Logger
}

// newAuthenticator 使用显式依赖注入创建认证组件。
func newAuthenticator(
	server authServer,
	subscriber loginCompletionSubscriber,
	browser browserOpener,
	getenv environmentLookup,
	logger *slog.Logger,
) *authenticator {
	if logger == nil {
		logger = slog.Default()
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return &authenticator{
		server: server, subscriber: subscriber, browser: browser, getenv: getenv, logger: logger,
	}
}

// codexAuthMethods 直接使用 ACP SDK AuthMethod union 公布 V1 认证方法。
func codexAuthMethods(browserEnabled bool) []acp.AuthMethod {
	apiDescription := "Use an API key to authenticate"
	methods := []acp.AuthMethod{{Agent: &acp.AuthMethodAgent{
		Id:          "api-key",
		Name:        "API Key",
		Description: &apiDescription,
		Meta: map[string]any{
			"api-key": map[string]any{"provider": "openai"},
		},
	}}}
	if browserEnabled {
		chatDescription := "Use ChatGPT to authenticate"
		methods = append(methods, acp.AuthMethod{Agent: &acp.AuthMethodAgent{
			Id: "chat-gpt", Name: "ChatGPT", Description: &chatDescription,
		}})
	}
	return methods
}

// Authenticate 按 methodId 选择固定 V1 认证流程，未知方法立即失败。
func (a *authenticator) Authenticate(ctx context.Context, request acp.AuthenticateRequest) error {
	if a.server == nil || a.subscriber == nil {
		return fmt.Errorf("authentication dependencies are unavailable")
	}
	switch request.MethodId {
	case "api-key":
		return a.authenticateAPIKey(ctx, request)
	case "chat-gpt":
		return a.authenticateChatGPT(ctx)
	default:
		return fmt.Errorf("unsupported authentication method %q", request.MethodId)
	}
}

// authenticateAPIKey 按 request meta、CODEX_API_KEY、OPENAI_API_KEY 顺序选择凭据。
func (a *authenticator) authenticateAPIKey(ctx context.Context, request acp.AuthenticateRequest) error {
	apiKey := apiKeyFromMeta(request.Meta)
	if apiKey == "" {
		apiKey = a.getenv("CODEX_API_KEY")
	}
	if apiKey == "" {
		apiKey = a.getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return errAPIKeyMissing
	}

	subscription, err := a.subscriber.SubscribeLoginCompleted()
	if err != nil {
		a.logger.Error("订阅登录完成通知失败")
		return errAuthenticationFailed
	}
	defer subscription.Close()
	if _, err := a.server.AccountLogin(ctx, protocol.LoginAccountParams{
		Type: protocol.TypeAPIKey, APIKey: &apiKey,
	}); err != nil {
		// 不包装上游错误：其文本可能包含服务端回显的 secret。
		a.logger.Error("API key 登录启动失败")
		return errAuthenticationFailed
	}
	return waitForLoginCompletion(ctx, subscription)
}

// authenticateChatGPT 先刷新账号；需要登录时严格执行 subscribe、start、open、wait 顺序。
func (a *authenticator) authenticateChatGPT(ctx context.Context) error {
	refresh := true
	account, err := a.server.AccountRead(ctx, protocol.GetAccountParams{RefreshToken: &refresh})
	if err != nil {
		a.logger.Error("读取 ChatGPT 账号失败")
		return errAuthenticationFailed
	}
	if account.Account != nil && account.Account.Type == protocol.AccountTypeChatgpt {
		return nil
	}

	subscription, err := a.subscriber.SubscribeLoginCompleted()
	if err != nil {
		a.logger.Error("订阅登录完成通知失败")
		return errAuthenticationFailed
	}
	defer subscription.Close()
	login, err := a.server.AccountLogin(ctx, protocol.LoginAccountParams{Type: protocol.TypeChatgpt})
	if err != nil {
		a.logger.Error("ChatGPT 登录启动失败")
		return errAuthenticationFailed
	}
	if login.Type != protocol.TypeChatgpt || login.AuthURL == nil || *login.AuthURL == "" || a.browser == nil {
		return errAuthenticationFailed
	}
	if err := a.browser.Open(ctx, *login.AuthURL); err != nil {
		a.logger.Error("打开 ChatGPT 登录页面失败")
		return errAuthenticationFailed
	}
	completion, waitErr := subscription.Wait(ctx)
	if waitErr != nil {
		if (errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded)) && login.LoginID != nil {
			// 取消请求不能复用已取消的 ctx，否则 app-server 无法收到清理信号。
			if _, cancelErr := a.server.AccountLoginCancel(
				context.WithoutCancel(ctx),
				protocol.CancelLoginAccountParams{LoginID: *login.LoginID},
			); cancelErr != nil {
				a.logger.Error("取消 ChatGPT 登录失败")
			}
		}
		return errAuthenticationFailed
	}
	if !completion.Success {
		return errAuthenticationFailed
	}
	return nil
}

// waitForLoginCompletion 等待 typed 通知并返回不包含上游 error 文本的稳定错误。
func waitForLoginCompletion(ctx context.Context, subscription loginCompletionSubscription) error {
	completion, err := subscription.Wait(ctx)
	if err != nil || !completion.Success {
		return errAuthenticationFailed
	}
	return nil
}

// apiKeyFromMeta 读取 upstream 兼容的 ACP `_meta.api-key.apiKey` 扩展。
func apiKeyFromMeta(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	value, ok := meta["api-key"]
	if !ok {
		return ""
	}
	switch apiMeta := value.(type) {
	case map[string]any:
		apiKey, _ := apiMeta["apiKey"].(string)
		return apiKey
	case map[string]string:
		return apiMeta["apiKey"]
	default:
		return ""
	}
}
