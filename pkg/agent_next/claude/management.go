package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const subscriptionAuth = "claudeai"

var claudeVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

type commandExit int

func (e commandExit) Error() string {
	return fmt.Sprintf("Claude command exited with status %d", int(e))
}

func (c *Client) environment(key string) string {
	for i := len(c.options.Environment) - 1; i >= 0; i-- {
		k, value, ok := strings.Cut(c.options.Environment[i], "=")
		if ok && (k == key || runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			return value
		}
	}
	return ""
}

func (c *Client) accountUsesEnvironment() bool {
	return c.environment("ANTHROPIC_API_KEY") != "" || c.environment("ANTHROPIC_AUTH_TOKEN") != "" || c.environment("CLAUDE_CODE_OAUTH_TOKEN") != ""
}

func (c *Client) authMethods() []acp.AuthMethod {
	if c.accountUsesEnvironment() {
		return []acp.AuthMethod{}
	}
	return []acp.AuthMethod{{Agent: &acp.AuthMethodAgent{Id: subscriptionAuth, Name: "Claude subscription"}}}
}

func (c *Client) authCapabilities() acp.AgentAuthCapabilities {
	if c.accountUsesEnvironment() {
		return acp.AgentAuthCapabilities{}
	}
	return acp.AgentAuthCapabilities{Logout: &acp.LogoutCapabilities{}}
}

func (c *Client) ready() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requireReadyLocked()
}

// Authenticate starts the subscription login supported by the frozen account.
func (c *Client) Authenticate(ctx context.Context, req acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	if err := c.ready(); err != nil {
		return acp.AuthenticateResponse{}, err
	}
	if req.MethodId != subscriptionAuth || c.accountUsesEnvironment() {
		return acp.AuthenticateResponse{}, acp.NewInvalidParams(map[string]any{"message": "Claude subscription login is unavailable for this credential"})
	}
	_, err := c.command(ctx, 5*time.Minute, "auth", "login", "--claudeai")
	return acp.AuthenticateResponse{}, err
}

// Logout removes the native subscription credential in the selected account directory.
func (c *Client) Logout(ctx context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	if err := c.ready(); err != nil {
		return acp.LogoutResponse{}, err
	}
	if c.accountUsesEnvironment() {
		return acp.LogoutResponse{}, acp.NewInvalidParams(map[string]any{"message": "the selected environment owns the Claude credential"})
	}
	_, err := c.command(ctx, startupTimeout, "auth", "logout")
	return acp.LogoutResponse{}, err
}

func (c *Client) managementContext(ctx context.Context, limit time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

func (c *Client) registerProcess(p *agentclient.Process) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errClosed
	}
	if len(c.management) >= maxSessions {
		return errCapacity
	}
	c.management[p] = struct{}{}
	return nil
}

func (c *Client) releaseProcess(ctx context.Context, p *agentclient.Process) error {
	if err := p.Close(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.management, p)
	c.signalLocked()
	c.mu.Unlock()
	return nil
}

func (c *Client) releaseManagement(p *agentclient.Process) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.options.ControlTimeout)
	defer cancel()
	return c.releaseProcess(ctx, p)
}

func (c *Client) command(ctx context.Context, limit time.Duration, args ...string) (output []byte, err error) {
	ctx, finish := c.managementContext(ctx, limit)
	defer finish()
	p, err := c.process(args, c.options.WorkingDirectory, c.options.Environment)
	if err != nil {
		return nil, err
	}
	if err := c.registerProcess(p); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, c.releaseManagement(p)) }()
	if err := p.Start(ctx); err != nil {
		return nil, err
	}
	_ = p.Stdin().Close()
	type readResult struct {
		data []byte
		err  error
	}
	read := make(chan readResult, 1)
	go func() { data, err := io.ReadAll(io.LimitReader(p.Stdout(), (64<<10)+1)); read <- readResult{data, err} }()
	select {
	case value := <-read:
		if value.err != nil {
			return nil, errors.New("Claude command stdout could not be read")
		}
		if len(value.data) > 64<<10 {
			return nil, errors.New("Claude command output exceeded limit")
		}
		output = value.data
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	exit, err := p.Wait(ctx)
	if err != nil {
		return output, err
	}
	if exit.Err != nil {
		return output, commandExit(exit.Code)
	}
	return output, nil
}

func (c *Client) probe(ctx context.Context, read func(context.Context, *wire, protocol.InitializeControlResponse) error) (err error) {
	if err := c.ready(); err != nil {
		return err
	}
	ctx, finish := c.managementContext(ctx, startupTimeout)
	defer finish()
	id, err := newUUID()
	if err != nil {
		return err
	}
	args := append(streamArgs(), "--setting-sources=", "--tools=", "--no-session-persistence", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--session-id="+id)
	p, err := c.process(args, c.options.WorkingDirectory, c.streamEnvironment())
	if err != nil {
		return err
	}
	if err := c.registerProcess(p); err != nil {
		return err
	}
	var transport *wire
	defer func() {
		err = errors.Join(err, c.releaseManagement(p))
		if transport != nil {
			bounded, cancel := context.WithTimeout(context.Background(), c.options.ControlTimeout)
			defer cancel()
			err = errors.Join(err, await(bounded, transport.done))
		}
	}()
	if err := p.Start(ctx); err != nil {
		return err
	}
	transport = startWire(p, c.options.ControlTimeout)
	go transport.read(func(protocol.Message) {}, func(error) {})
	var response protocol.InitializeControlResponse
	if err := transport.call(ctx, protocol.InitializeControlRequest{Subtype: "initialize"}, &response, nil, nil); err != nil {
		return err
	}
	return read(ctx, transport, response)
}

// ReadModels returns model capabilities discovered by an isolated, prompt-free process.
func (c *Client) ReadModels(ctx context.Context) (acpmeta.ModelCatalog, error) {
	var result acpmeta.ModelCatalog
	err := c.probe(ctx, func(_ context.Context, _ *wire, init protocol.InitializeControlResponse) error {
		if init.Models == nil {
			return errors.New("Claude did not report a model catalog")
		}
		rows := modelOptions(init.Models)
		result.Options.Ungrouped = &rows
		for _, row := range init.Models {
			if row.Value == "default" && row.ResolvedModel != "" {
				result.DefaultID = acp.Ptr(acp.SessionConfigValueId(modelID(row)))
				break
			}
		}
		return nil
	})
	return result, err
}

// ReadAccount obtains the native account observation without opening a session.
func (c *Client) ReadAccount(ctx context.Context) (acpmeta.AccountInfo, error) {
	if err := c.ready(); err != nil {
		return acpmeta.AccountInfo{}, err
	}
	output, err := c.command(ctx, 5*time.Second, "auth", "status", "--json")
	status := decodeOptional[struct {
		LoggedIn *bool   `json:"loggedIn"`
		Method   string  `json:"authMethod"`
		Provider string  `json:"apiProvider"`
		Email    *string `json:"email"`
		Plan     *string `json:"subscriptionType"`
	}](output)
	var exited commandExit
	if err != nil && !(errors.As(err, &exited) && exited == 1 && status != nil && status.LoggedIn != nil && !*status.LoggedIn) {
		return acpmeta.AccountInfo{}, err
	}
	if status == nil || status.LoggedIn == nil {
		return acpmeta.AccountInfo{}, errors.New("Claude returned unreadable account status")
	}
	if !*status.LoggedIn {
		return acpmeta.AccountInfo{RequiresAuthentication: true}, nil
	}
	account := &acpmeta.AccountIdentity{Kind: "unknown"}
	switch status.Method {
	case "claude.ai", "claudeai", "oauth":
		account.Kind, account.Label, account.Plan = "subscription", status.Email, status.Plan
	case "apiKey", "api_key", "anthropicConsole":
		account.Kind = "api_key"
	default:
		if status.Provider == "bedrock" || status.Provider == "vertex" || status.Provider == "foundry" {
			account.Kind = status.Provider
		}
	}
	return acpmeta.AccountInfo{Account: account}, nil
}

// ReadAccountUsage reads native subscription windows separately from turn usage.
func (c *Client) ReadAccountUsage(ctx context.Context) (acpmeta.AccountUsage, error) {
	var native protocol.AccountUsageResponse
	err := c.probe(ctx, func(ctx context.Context, w *wire, _ protocol.InitializeControlResponse) error {
		return w.call(ctx, protocol.GetUsageControlRequest{Subtype: "get_usage", SkipBehaviors: true}, &native, nil, nil)
	})
	if err != nil {
		return acpmeta.AccountUsage{}, err
	}
	result := acpmeta.AccountUsage{Available: native.Available}
	if native.Available == nil || !*native.Available || native.RateLimits == nil {
		return result, nil
	}
	bucket := acpmeta.AccountUsageBucket{ID: "rate_limits", Windows: []acpmeta.AccountUsageWindow{}}
	result.Buckets = []acpmeta.AccountUsageBucket{}
	for _, key := range slices.Sorted(maps.Keys(native.RateLimits)) {
		raw := native.RateLimits[key]
		if string(raw) == "null" {
			continue
		}
		switch key {
		case "model_scoped":
			rows := decodeOptional[[]protocol.QuotaWindow](raw)
			if rows == nil {
				return acpmeta.AccountUsage{}, errors.New("invalid Claude model quota")
			}
			result.ModelBuckets = make([]acpmeta.AccountUsageBucket, 0, len(*rows))
			for _, row := range *rows {
				model := acpmeta.AccountUsageBucket{Windows: []acpmeta.AccountUsageWindow{quotaWindow("seven_day", row)}}
				if row.Name != "" {
					model.Name = acp.Ptr(row.Name)
				}
				result.ModelBuckets = append(result.ModelBuckets, model)
			}
		case "extra_usage":
			row := decodeOptional[protocol.ExtraUsage](raw)
			if row == nil {
				return acpmeta.AccountUsage{}, errors.New("invalid Claude extra usage")
			}
			extra := acpmeta.AccountUsageBucket{ID: key, Windows: []acpmeta.AccountUsageWindow{}, Extra: &acpmeta.AccountUsageExtra{
				Enabled: row.Enabled, Used: row.Used, Limit: row.Limit, Currency: row.Currency,
			}}
			if row.Utilization != nil {
				extra.Windows = append(extra.Windows, acpmeta.AccountUsageWindow{Kind: key, UsedPercent: row.Utilization})
			}
			result.Buckets = append(result.Buckets, extra)
		default:
			row := decodeOptional[protocol.QuotaWindow](raw)
			if row == nil {
				continue
			}
			bucket.Windows = append(bucket.Windows, quotaWindow(key, *row))
		}
	}
	if len(bucket.Windows) > 0 {
		result.Buckets = append([]acpmeta.AccountUsageBucket{bucket}, result.Buckets...)
	}
	return result, nil
}

func quotaWindow(kind string, row protocol.QuotaWindow) acpmeta.AccountUsageWindow {
	value := acpmeta.AccountUsageWindow{Kind: kind, UsedPercent: row.Utilization}
	switch {
	case kind == "five_hour":
		value.DurationSeconds = acp.Ptr(int64(5 * 3600))
	case kind == "seven_day" || strings.HasPrefix(kind, "seven_day_"):
		value.DurationSeconds = acp.Ptr(int64(7 * 24 * 3600))
	}
	if row.ResetAt != nil {
		if t, err := time.Parse(time.RFC3339Nano, *row.ResetAt); err == nil {
			value.ResetAtUnixSeconds = acp.Ptr(t.Unix())
		}
	}
	return value
}

// HandleExtensionMethod decodes the existing Claude management endpoints for ACP.
func (c *Client) HandleExtensionMethod(ctx context.Context, method string, _ json.RawMessage) (any, error) {
	switch method {
	case acpmeta.ModelCatalogMethod:
		return c.ReadModels(ctx)
	case acpmeta.AccountReadMethod:
		return c.ReadAccount(ctx)
	case acpmeta.AccountUsageMethod:
		return c.ReadAccountUsage(ctx)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}
