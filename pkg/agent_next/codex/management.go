package codex

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func (c *Client) readyError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checkReadyLocked()
}

func (c *Client) readModelPages(ctx context.Context) ([]protocol.ModelListResponseDatum, error) {
	models := make([]protocol.ModelListResponseDatum, 0)
	seenIDs := make(map[string]bool)
	seenCursors := make(map[string]bool)
	params := protocol.ModelListParams{Limit: pointer(int64(100))}
	for page := 0; page < 100; page++ {
		var reply protocol.ModelListResponse
		if err := c.request(ctx, "model/list", params, &reply); err != nil {
			return nil, err
		}
		for _, model := range reply.Data {
			id := modelID(model)
			if id != "" && !model.Hidden && !seenIDs[id] {
				models = append(models, model)
				seenIDs[id] = true
			}
		}
		if reply.NextCursor == nil || *reply.NextCursor == "" {
			return models, nil
		}
		if seenCursors[*reply.NextCursor] {
			return nil, errors.New("Codex repeated a model catalog cursor")
		}
		seenCursors[*reply.NextCursor] = true
		params.Cursor = reply.NextCursor
	}
	return nil, errors.New("Codex model catalog exceeded page limit")
}

func (c *Client) ReadModels(ctx context.Context) (acpmeta.ModelCatalog, error) {
	if err := c.readyError(); err != nil {
		return acpmeta.ModelCatalog{}, err
	}
	models, err := c.readModelPages(ctx)
	if err != nil {
		return acpmeta.ModelCatalog{}, err
	}
	options := make([]acp.SessionConfigSelectOption, 0, len(models))
	var defaultID *acp.SessionConfigValueId
	for _, model := range models {
		options = append(options, modelChoice(model))
		if model.IsDefault {
			defaultID = pointer(acp.SessionConfigValueId(modelID(model)))
		}
	}
	return acpmeta.ModelCatalog{DefaultID: defaultID, Options: acp.SessionConfigSelectOptions{Ungrouped: pointer(acp.SessionConfigSelectOptionsUngrouped(options))}}, nil
}

func modelID(model protocol.ModelListResponseDatum) string {
	if model.Model != "" {
		return model.Model
	}
	return model.ID
}

func lookupModel(models []protocol.ModelListResponseDatum, id string) *protocol.ModelListResponseDatum {
	for i := range models {
		if modelID(models[i]) == id {
			return &models[i]
		}
	}
	return nil
}

func modelChoice(model protocol.ModelListResponseDatum) acp.SessionConfigSelectOption {
	id := modelID(model)
	name := model.DisplayName
	if name == "" {
		name = id
	}
	meta := map[string]any{}
	for _, tier := range modelTiers(&model) {
		if strings.EqualFold(strings.TrimSpace(tier.Name), "fast") {
			meta[fastServiceTierKey] = tier.ID
			break
		}
	}
	settings := make([]acp.SessionConfigOption, 0, 2)
	if efforts := effortChoices(&model); len(efforts) > 0 {
		settings = append(settings, selectOption("reasoning_effort", "Reasoning effort", "thought_level", model.DefaultReasoningEffort, efforts))
	}
	if tiers := tierChoices(&model); len(tiers) > 0 {
		settings = append(settings, selectOption("service_tier", "Service tier", "", "inherit", tiers))
	}
	return acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(id), Name: name, Description: optionalText(model.Description), Meta: acpmeta.WithModelConfigOptions(meta, settings)}
}

func modelTiers(model *protocol.ModelListResponseDatum) []protocol.ServiceTierElement {
	if model == nil {
		return nil
	}
	if len(model.ServiceTiers) > 0 {
		return model.ServiceTiers
	}
	if slices.Contains(model.AdditionalSpeedTiers, "fast") {
		return []protocol.ServiceTierElement{{ID: "fast", Name: "Fast"}}
	}
	return nil
}

func supportsEffort(model *protocol.ModelListResponseDatum, effort string) bool {
	return model != nil && slices.ContainsFunc(model.SupportedReasoningEfforts, func(e protocol.SupportedReasoningEffortElement) bool { return e.ReasoningEffort == effort })
}

func supportsTier(model *protocol.ModelListResponseDatum, id string) bool {
	return slices.ContainsFunc(modelTiers(model), func(t protocol.ServiceTierElement) bool { return t.ID == id })
}

func selectOption(id, name, category, current string, options []acp.SessionConfigSelectOption) acp.SessionConfigOption {
	var kind *acp.SessionConfigOptionCategory
	if category != "" {
		kind = pointer(acp.SessionConfigOptionCategory(category))
	}
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: acp.SessionConfigId(id), Name: name, Category: kind, CurrentValue: acp.SessionConfigValueId(current),
		Options: acp.SessionConfigSelectOptions{Ungrouped: pointer(acp.SessionConfigSelectOptionsUngrouped(options))}, Type: "select",
	}}
}

func effortChoices(model *protocol.ModelListResponseDatum) []acp.SessionConfigSelectOption {
	if model == nil {
		return nil
	}
	options := make([]acp.SessionConfigSelectOption, 0, len(model.SupportedReasoningEfforts))
	for _, effort := range model.SupportedReasoningEfforts {
		options = append(options, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(effort.ReasoningEffort), Name: effort.ReasoningEffort, Description: optionalText(effort.Description)})
	}
	return options
}

func tierChoices(model *protocol.ModelListResponseDatum) []acp.SessionConfigSelectOption {
	tiers := modelTiers(model)
	if len(tiers) == 0 {
		return nil
	}
	options := []acp.SessionConfigSelectOption{{Value: "inherit", Name: "Inherit"}, {Value: "off", Name: "Off"}}
	for _, tier := range tiers {
		if tier.ID != "" {
			options = append(options, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(tier.ID), Name: tier.Name, Description: optionalText(tier.Description)})
		}
	}
	return options
}

func (s *session) configOptions() []acp.SessionConfigOption {
	modes := make([]acp.SessionConfigSelectOption, 0, 3)
	for _, mode := range modeState(s.mode).AvailableModes {
		modes = append(modes, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(mode.Id), Name: mode.Name})
	}
	options := []acp.SessionConfigOption{selectOption("mode", "Mode", "mode", string(s.mode), modes)}
	current := s.currentModel()
	model := lookupModel(s.models, current)
	if current == "" {
		return options
	}
	models := make([]acp.SessionConfigSelectOption, 0, len(s.models)+1)
	for _, entry := range s.models {
		models = append(models, modelChoice(entry))
	}
	if model == nil {
		models = append(models, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(current), Name: current})
	}
	options = append(options, selectOption("model", "Model", "model", current, models))
	if efforts := effortChoices(model); len(efforts) > 0 {
		effort := value(s.settings.Requested.ReasoningEffort)
		if effort == "" {
			effort = value(s.settings.Observed.ReasoningEffort)
		}
		if !supportsEffort(model, effort) {
			effort = model.DefaultReasoningEffort
		}
		options = append(options, selectOption("reasoning_effort", "Reasoning effort", "thought_level", effort, efforts))
	}
	if tiers := tierChoices(model); len(tiers) > 0 {
		tier := "inherit"
		if selected, ok := s.settings.Requested.ServiceTier.Value(); ok {
			tier = selected
		} else if s.settings.Requested.ServiceTier.IsNull() {
			tier = "off"
		}
		options = append(options, selectOption("service_tier", "Service tier", "", tier, tiers))
	}
	return options
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (c *Client) readAccount(ctx context.Context, refresh bool) (protocol.GetAccountResponse, error) {
	var result protocol.GetAccountResponse
	if err := c.readyError(); err != nil {
		return result, err
	}
	err := c.request(ctx, "account/read", protocol.GetAccountParams{RefreshToken: &refresh}, &result)
	return result, err
}

func (c *Client) ReadAccount(ctx context.Context) (acpmeta.AccountInfo, error) {
	r, err := c.readAccount(ctx, false)
	result := acpmeta.AccountInfo{RequiresAuthentication: r.RequiresOpenaiAuth}
	if err != nil || r.Account == nil {
		return result, err
	}
	result.Account = &acpmeta.AccountIdentity{Kind: "unknown", Label: r.Account.Email}
	switch r.Account.Type {
	case protocol.AccountTypeAPIKey:
		result.Account.Kind = "api_key"
	case protocol.AccountTypeChatgpt:
		result.Account.Kind = "account"
	}
	if r.Account.PlanType != nil {
		result.Account.Plan = pointer(string(*r.Account.PlanType))
	}
	return result, nil
}

func (c *Client) checkAccount(ctx context.Context) error {
	account, err := c.ReadAccount(ctx)
	if err == nil && account.RequiresAuthentication && account.Account == nil {
		err = acp.NewAuthRequired(nil)
	}
	return err
}

func (c *Client) authMethods() []acp.AuthMethod {
	methods := []acp.AuthMethod{{Agent: &acp.AuthMethodAgent{Id: "api-key", Name: "API Key", Meta: map[string]any{"api-key": map[string]any{"provider": "openai"}}}}}
	if c.getenv("NO_BROWSER") == "" {
		methods = append(methods, acp.AuthMethod{Agent: &acp.AuthMethodAgent{Id: "chat-gpt", Name: "ChatGPT"}})
	}
	return methods
}

func (c *Client) getenv(name string) string {
	for _, entry := range slices.Backward(c.options.Environment) {
		if key, val, ok := strings.Cut(entry, "="); ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			return val
		}
	}
	return ""
}

func (c *Client) Authenticate(ctx context.Context, r acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	if err := c.readyError(); err != nil {
		return acp.AuthenticateResponse{}, err
	}
	var params protocol.LoginAccountParams
	switch r.MethodId {
	case "api-key":
		var api struct {
			Key string `json:"apiKey"`
		}
		if raw, err := json.Marshal(r.Meta["api-key"]); err == nil {
			_ = json.Unmarshal(raw, &api)
		}
		if api.Key == "" {
			api.Key = c.getenv("CODEX_API_KEY")
		}
		if api.Key == "" {
			api.Key = c.getenv("OPENAI_API_KEY")
		}
		if api.Key == "" {
			return acp.AuthenticateResponse{}, acp.NewInvalidParams("OpenAI API key is required")
		}
		params = protocol.LoginAccountParams{Type: protocol.TypeAPIKey, APIKey: &api.Key}
	case "chat-gpt":
		if c.getenv("NO_BROWSER") != "" {
			return acp.AuthenticateResponse{}, acp.NewInvalidParams("Codex browser login is disabled")
		}
		account, err := c.readAccount(ctx, true)
		if err != nil {
			return acp.AuthenticateResponse{}, errors.New("Codex could not read login state")
		}
		if account.Account != nil && account.Account.Type == protocol.AccountTypeChatgpt {
			return acp.AuthenticateResponse{}, nil
		}
		params.Type = protocol.TypeChatgpt
	default:
		return acp.AuthenticateResponse{}, acp.NewInvalidParams("unknown Codex authentication method")
	}
	c.mu.Lock()
	if c.login != nil || c.loggingOut {
		c.mu.Unlock()
		return acp.AuthenticateResponse{}, errors.New("Codex authentication already in progress")
	}
	pending := make(chan protocol.AccountLoginCompletedNotification, 8)
	c.login = pending
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.login = nil; c.mu.Unlock() }()
	var login protocol.LoginAccountResponse
	if err := c.request(ctx, "account/login/start", params, &login); err != nil {
		return acp.AuthenticateResponse{}, errors.New("Codex login could not start")
	}
	loginID := value(login.LoginID)
	completed := false
	if loginID != "" {
		defer func() {
			if completed {
				return
			}
			cancelCtx, cancel := context.WithTimeout(context.Background(), controlTimeout)
			defer cancel()
			_ = c.request(cancelCtx, "account/login/cancel", protocol.CancelLoginAccountParams{LoginID: loginID}, nil)
		}()
	}
	if params.Type == protocol.TypeChatgpt {
		if loginID == "" || login.Type != params.Type || openLoginURL(ctx, value(login.AuthURL)) != nil {
			return acp.AuthenticateResponse{}, errors.New("Codex browser login could not start")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		select {
		case event := <-pending:
			if value(event.LoginID) != loginID {
				continue
			}
			completed = true
			if !event.Success {
				return acp.AuthenticateResponse{}, errors.New("Codex login failed")
			}
			return acp.AuthenticateResponse{}, nil
		case <-ctx.Done():
			return acp.AuthenticateResponse{}, ctx.Err()
		case <-c.life.Done():
			return acp.AuthenticateResponse{}, context.Cause(c.life)
		}
	}
}

func openLoginURL(ctx context.Context, address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("invalid Codex authentication URL")
	}
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{address}
	case "linux":
		name, args = "xdg-open", []string{address}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", address}
	default:
		return errors.New("Codex browser authentication is unavailable on this platform")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (c *Client) captureLoginLocked(raw json.RawMessage) {
	var event protocol.AccountLoginCompletedNotification
	if c.login != nil && json.Unmarshal(raw, &event) == nil {
		select {
		case c.login <- event:
		default:
		}
	}
}

func (c *Client) Logout(ctx context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	if err := c.readyError(); err != nil {
		return acp.LogoutResponse{}, err
	}
	c.mu.Lock()
	if c.login != nil || c.loggingOut {
		c.mu.Unlock()
		return acp.LogoutResponse{}, errors.New("Codex account operation already in progress")
	}
	c.loggingOut = true
	done := make(chan struct{})
	c.logoutDone = done
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.loggingOut = false; c.logoutDone = nil; c.mu.Unlock() }()
	if err := c.request(ctx, "account/logout", nil, nil); err != nil {
		return acp.LogoutResponse{}, err
	}
	wait, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	select {
	case <-done:
		return acp.LogoutResponse{}, nil
	case <-wait.Done():
		return acp.LogoutResponse{}, wait.Err()
	case <-c.life.Done():
		return acp.LogoutResponse{}, context.Cause(c.life)
	}
}

func (c *Client) ReadAccountUsage(ctx context.Context) (acpmeta.AccountUsage, error) {
	if err := c.readyError(); err != nil {
		return acpmeta.AccountUsage{}, err
	}
	var native struct {
		Single  *protocol.RateLimits                 `json:"rateLimits"`
		Buckets map[string]protocol.RateLimits       `json:"rateLimitsByLimitId"`
		Credits *protocol.RateLimitResetCreditsClass `json:"rateLimitResetCredits"`
	}
	if err := c.request(ctx, "account/rateLimits/read", struct{}{}, &native); err != nil {
		return acpmeta.AccountUsage{}, err
	}
	result := acpmeta.AccountUsage{}
	if native.Credits != nil {
		result.ResetCredits = &acpmeta.AccountUsageResetCredits{AvailableCount: native.Credits.AvailableCount}
		if native.Credits.Credits != nil {
			result.ResetCredits.Details = make([]acpmeta.AccountUsageResetCredit, 0, len(native.Credits.Credits))
			for _, credit := range native.Credits.Credits {
				if credit.ID == "" {
					return acpmeta.AccountUsage{}, errors.New("Codex reset credit has no identity")
				}
				result.ResetCredits.Details = append(result.ResetCredits.Details, acpmeta.AccountUsageResetCredit{
					ID: credit.ID, Status: string(credit.Status), ResetType: string(credit.ResetType), Title: credit.Title,
					Description: credit.Description, GrantedAtUnixSeconds: credit.GrantedAt, ExpiresAtUnixSeconds: credit.ExpiresAt,
				})
			}
		}
	}
	if native.Buckets == nil && native.Single != nil {
		id := value(native.Single.LimitID)
		if id == "" {
			id = "codex"
		}
		native.Buckets = map[string]protocol.RateLimits{id: *native.Single}
	}
	if native.Buckets != nil {
		result.Buckets = make([]acpmeta.AccountUsageBucket, 0, len(native.Buckets))
		for _, id := range slices.Sorted(maps.Keys(native.Buckets)) {
			bucket, err := quotaBucket(id, native.Buckets[id])
			if err != nil {
				return acpmeta.AccountUsage{}, err
			}
			result.Buckets = append(result.Buckets, bucket)
		}
	}
	return result, nil
}

func quotaBucket(id string, native protocol.RateLimits) (acpmeta.AccountUsageBucket, error) {
	bucket := acpmeta.AccountUsageBucket{ID: id, Name: native.LimitName, Windows: []acpmeta.AccountUsageWindow{}, SpendControlReached: native.SpendControlReached}
	if native.RateLimitReachedType != nil {
		bucket.ReachedType = pointer(string(*native.RateLimitReachedType))
	}
	if credit := native.Credits; credit != nil {
		bucket.HasCredits, bucket.Unlimited, bucket.Balance = &credit.HasCredits, &credit.Unlimited, credit.Balance
	}
	percent := func(raw *int64) (*float64, error) {
		if raw == nil {
			return nil, nil
		}
		if *raw > 1<<53 || *raw < -(1<<53) {
			return nil, errors.New("Codex quota percentage is out of range")
		}
		return pointer(float64(*raw)), nil
	}
	if item := native.IndividualLimit; item != nil {
		remaining, err := percent(item.RemainingPercent)
		if err != nil {
			return bucket, err
		}
		bucket.Windows = append(bucket.Windows, acpmeta.AccountUsageWindow{Kind: "individual", RemainingPercent: remaining, ResetAtUnixSeconds: item.ResetsAt})
	}
	for i, item := range []*protocol.PrimaryClass{native.Primary, native.Secondary} {
		if item == nil {
			continue
		}
		window := acpmeta.AccountUsageWindow{Kind: []string{"primary", "secondary"}[i], ResetAtUnixSeconds: item.ResetsAt}
		pct, err := percent(item.UsedPercent)
		if err != nil {
			return bucket, err
		}
		window.UsedPercent = pct
		if item.WindowDurationMins != nil {
			minutes := *item.WindowDurationMins
			if minutes < 0 || minutes > math.MaxInt64/60 {
				return bucket, errors.New("Codex quota duration is out of range")
			}
			window.DurationSeconds = pointer(minutes * 60)
		}
		bucket.Windows = append(bucket.Windows, window)
	}
	return bucket, nil
}

func (c *Client) HandleExtensionMethod(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case acpmeta.ModelCatalogMethod:
		return c.ReadModels(ctx)
	case acpmeta.AccountReadMethod:
		return c.ReadAccount(ctx)
	case acpmeta.AccountUsageMethod:
		return c.ReadAccountUsage(ctx)
	case SessionSettingsMethod:
		var input struct {
			ID acp.SessionId `json:"session_id"`
		}
		if json.Unmarshal(raw, &input) != nil || input.ID == "" {
			return nil, acp.NewInvalidParams("session_id is required")
		}
		return c.ReadSessionSettings(ctx, input.ID)
	case ThreadArchiveMethod, ThreadUnarchiveMethod:
		var input struct {
			ID string `json:"thread_id"`
		}
		if json.Unmarshal(raw, &input) != nil || input.ID == "" {
			return nil, acp.NewInvalidParams("thread_id is required")
		}
		return c.SetThreadArchived(ctx, input.ID, method == ThreadArchiveMethod)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}
