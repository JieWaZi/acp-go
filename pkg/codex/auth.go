package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

var (
	// errAuthenticationFailed 是不包含服务端详情或凭据的认证失败哨兵错误。
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
	// Wait accepts only the exact expected native login ID, or empty for the API key flow.
	Wait(context.Context, string) (protocol.AccountLoginCompletedNotification, error)
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

// browserAuthEnabled 在 NO_BROWSER 为空时允许浏览器认证。
func (a *authenticator) browserAuthEnabled() bool {
	return a != nil && a.getenv != nil && a.getenv("NO_BROWSER") == ""
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

// authenticateAPIKey selects request metadata before CODEX_API_KEY and OPENAI_API_KEY.
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
		a.logger.Error("Failed to subscribe to login completion notifications")
		return codexAuthenticationFailure("account_login_subscribe", err)
	}
	defer subscription.Close()
	if _, err := a.server.AccountLogin(ctx, protocol.LoginAccountParams{
		Type: protocol.TypeAPIKey, APIKey: &apiKey,
	}); err != nil {
		// Native error text may contain the echoed credential.
		a.logger.Error("Failed to start API key login")
		return codexAuthenticationFailure("account_login_start", err)
	}
	completion, err := subscription.Wait(ctx, "")
	if err != nil {
		return codexAuthenticationFailure("account_login_completion", err)
	}
	if !completion.Success {
		return codexAuthenticationFailure("account_login_completion", errAuthenticationFailed)
	}
	return nil
}

// authenticateChatGPT joins the completion for this exact browser login attempt.
func (a *authenticator) authenticateChatGPT(ctx context.Context) error {
	refresh := true
	account, err := a.server.AccountRead(ctx, protocol.GetAccountParams{RefreshToken: &refresh})
	if err != nil {
		return codexAuthenticationFailure("account_read", err)
	}
	if account.Account != nil && account.Account.Type == protocol.AccountTypeChatgpt {
		return nil
	}
	subscription, err := a.subscriber.SubscribeLoginCompleted()
	if err != nil {
		return codexAuthenticationFailure("account_login_subscribe", err)
	}
	defer subscription.Close()
	login, err := a.server.AccountLogin(ctx, protocol.LoginAccountParams{Type: protocol.TypeChatgpt})
	if err != nil {
		return codexAuthenticationFailure("account_login_start", err)
	}
	loginID := stringValue(login.LoginID)
	finished := false
	if loginID != "" {
		defer func() {
			if finished {
				return
			}
			cleanupCtx, cancelCleanup := newAppServerCleanupContext(ctx)
			defer cancelCleanup()
			if _, err := a.server.AccountLoginCancel(cleanupCtx, protocol.CancelLoginAccountParams{LoginID: loginID}); err != nil {
				a.logger.Error("Failed to cancel ChatGPT login")
			}
		}()
	}
	if login.Type != protocol.TypeChatgpt || loginID == "" || stringValue(login.AuthURL) == "" || a.browser == nil {
		return codexAuthenticationFailure("account_login_start", errAuthenticationFailed)
	}
	if err := a.browser.Open(ctx, *login.AuthURL); err != nil {
		return codexAuthenticationFailure("account_login_browser", err)
	}
	completion, err := subscription.Wait(ctx, loginID)
	if err != nil {
		return codexAuthenticationFailure("account_login_completion", err)
	}
	finished = true
	if !completion.Success {
		return codexAuthenticationFailure("account_login_completion", errAuthenticationFailed)
	}
	return nil
}

// codexAuthenticationFailure retains phase and native code without echoing credentials.
func codexAuthenticationFailure(phase string, err error) *acp.RequestError {
	failure := codexRequestFailure(phase, err)
	data := failure.Data.(map[string]any)
	if native, ok := data["native"].(map[string]any); ok {
		data["native"] = map[string]any{"code": native["code"]}
	}
	failure.Message = errAuthenticationFailed.Error()
	return failure
}

// apiKeyFromMeta 读取 ACP `_meta.api-key.apiKey` 扩展。
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

// AccountUsage reads and normalizes quota without starting a session or prompt.
func (a *Agent) AccountUsage(ctx context.Context) (acpmeta.AccountUsage, error) {
	if err := a.requireInitialized(); err != nil {
		return acpmeta.AccountUsage{}, err
	}
	response, known, err := a.client.AccountRateLimitsRead(ctx)
	if err != nil {
		return acpmeta.AccountUsage{}, codexRequestFailure("account_usage", err)
	}
	result := acpmeta.AccountUsage{}
	if credits := response.RateLimitResetCredits; credits != nil {
		result.ResetCredits = &acpmeta.AccountUsageResetCredits{AvailableCount: credits.AvailableCount}
		if credits.Credits != nil {
			result.ResetCredits.Details = make([]acpmeta.AccountUsageResetCredit, 0, len(credits.Credits))
			for _, credit := range credits.Credits {
				if credit.ID == "" {
					return acpmeta.AccountUsage{}, codexRequestFailure("account_usage_decode", errors.New("Codex reset credit has no native identity"))
				}
				result.ResetCredits.Details = append(result.ResetCredits.Details, acpmeta.AccountUsageResetCredit{
					ID: credit.ID, Status: string(credit.Status), ResetType: string(credit.ResetType),
					Title: credit.Title, Description: credit.Description, GrantedAtUnixSeconds: credit.GrantedAt, ExpiresAtUnixSeconds: credit.ExpiresAt,
				})
			}
		}
	}
	if !known {
		return result, nil
	}
	result.Buckets = []acpmeta.AccountUsageBucket{}
	appendBucket := func(id string, native protocol.RateLimits) error {
		bucket := acpmeta.AccountUsageBucket{ID: id, Name: native.LimitName, SpendControlReached: native.SpendControlReached}
		if native.RateLimitReachedType != nil {
			reached := string(*native.RateLimitReachedType)
			bucket.ReachedType = &reached
		}
		if individual := native.IndividualLimit; individual != nil {
			item := acpmeta.AccountUsageWindow{Kind: "individual", ResetAtUnixSeconds: individual.ResetsAt}
			if individual.RemainingPercent != nil {
				value := *individual.RemainingPercent
				if value > 1<<53 || value < -(1<<53) {
					return errors.New("native quota percentage exceeds exact numeric range")
				}
				percentage := float64(value)
				item.RemainingPercent = &percentage
			}
			bucket.Windows = append(bucket.Windows, item)
		}
		if native.Credits != nil {
			bucket.HasCredits = &native.Credits.HasCredits
			bucket.Unlimited = &native.Credits.Unlimited
			bucket.Balance = native.Credits.Balance
		}
		for _, window := range []struct {
			// kind identifies the reported window.
			kind string
			// value contains the native window facts.
			value *protocol.PrimaryClass
		}{{"primary", native.Primary}, {"secondary", native.Secondary}} {
			if window.value == nil {
				continue
			}
			item := acpmeta.AccountUsageWindow{Kind: window.kind, ResetAtUnixSeconds: window.value.ResetsAt}
			if window.value.UsedPercent != nil {
				value := *window.value.UsedPercent
				if value > 1<<53 || value < -(1<<53) {
					return fmt.Errorf("native quota percentage exceeds exact numeric range")
				}
				percentage := float64(value)
				item.UsedPercent = &percentage
			}
			if window.value.WindowDurationMins != nil {
				minutes := *window.value.WindowDurationMins
				if minutes > math.MaxInt64/60 || minutes < math.MinInt64/60 {
					return fmt.Errorf("native quota duration exceeds numeric range")
				}
				seconds := minutes * 60
				item.DurationSeconds = &seconds
			}
			bucket.Windows = append(bucket.Windows, item)
		}
		result.Buckets = append(result.Buckets, bucket)
		return nil
	}
	if response.RateLimitsByLimitID != nil {
		ids := make([]string, 0, len(response.RateLimitsByLimitID))
		for id := range response.RateLimitsByLimitID {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := appendBucket(id, response.RateLimitsByLimitID[id]); err != nil {
				return acpmeta.AccountUsage{}, err
			}
		}
	} else {
		id := "codex"
		if response.RateLimits.LimitID != nil {
			id = *response.RateLimits.LimitID
		}
		if err := appendBucket(id, response.RateLimits); err != nil {
			return acpmeta.AccountUsage{}, err
		}
	}
	return result, nil
}

// Authenticate 把 ACP 认证请求交给现有 authenticator，不在 Agent 重写登录状态机。
func (a *Agent) Authenticate(
	ctx context.Context,
	request acp.AuthenticateRequest,
) (acp.AuthenticateResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.AuthenticateResponse{}, err
	}
	if a.auth == nil {
		return acp.AuthenticateResponse{}, errors.New("codex authenticator is unavailable")
	}
	if err := a.auth.Authenticate(ctx, request); err != nil {
		return acp.AuthenticateResponse{}, err
	}
	return acp.AuthenticateResponse{}, nil
}

// checkAuthorization 在创建或恢复 Session 前复用 app-server 的无刷新账号检查。
func (a *Agent) checkAuthorization(ctx context.Context) error {
	refresh := false
	account, err := a.client.AccountRead(ctx, protocol.GetAccountParams{RefreshToken: &refresh})
	if err != nil {
		return codexAuthenticationFailure("account_read", err)
	}
	if account.RequiresOpenaiAuth && account.Account == nil {
		return acp.NewAuthRequired(nil)
	}
	return nil
}

// Logout 先订阅账号更新，再发送登出请求并等待通知，避免丢失快速响应。
func (a *Agent) Logout(ctx context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	if err := a.requireInitialized(); err != nil {
		return acp.LogoutResponse{}, err
	}
	subscription := a.client.SubscribeAccountUpdated()
	defer subscription.Close()
	if err := a.client.AccountLogout(ctx); err != nil {
		return acp.LogoutResponse{}, codexAuthenticationFailure("account_logout", err)
	}
	if err := subscription.Wait(ctx); err != nil {
		return acp.LogoutResponse{}, codexAuthenticationFailure("account_logout_update", err)
	}
	return acp.LogoutResponse{}, nil
}

// AccountInfo reads the native account and normalizes only reported identity facts.
func (a *Agent) AccountInfo(ctx context.Context) (acpmeta.AccountInfo, error) {
	if err := a.requireInitialized(); err != nil {
		return acpmeta.AccountInfo{}, err
	}
	refresh := false
	native, err := a.client.AccountRead(ctx, protocol.GetAccountParams{RefreshToken: &refresh})
	if err != nil {
		return acpmeta.AccountInfo{}, codexRequestFailure("account_read", err)
	}
	result := acpmeta.AccountInfo{RequiresAuthentication: native.RequiresOpenaiAuth}
	if native.Account == nil {
		return result, nil
	}
	account := &acpmeta.AccountIdentity{Kind: "unknown", Label: native.Account.Email}
	switch native.Account.Type {
	case protocol.AccountTypeAPIKey:
		account.Kind = "api_key"
	case protocol.AccountTypeChatgpt:
		account.Kind = "account"
	}
	if native.Account.PlanType != nil {
		plan := string(*native.Account.PlanType)
		account.Plan = &plan
	}
	result.Account = account
	return result, nil
}
