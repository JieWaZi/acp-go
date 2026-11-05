package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/claude/protocol"
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
			if row.Value == "default" {
				result.DefaultID = acp.Ptr(acp.SessionConfigValueId(modelID(row)))
				break
			}
		}
		return nil
	})
	return result, err
}

// LoginStatus is the native account observation for the frozen environment.
type LoginStatus struct {
	// Authenticated reports whether the CLI recognizes a selected credential.
	Authenticated bool
	// Kind distinguishes subscriptions, API keys and external cloud credentials.
	Kind string
	// AuthMethod is the recognized native sign-in method, separate from the inferred Kind.
	AuthMethod string
	// Email is the subscription label when reported.
	Email *string
	// Plan is the subscription type when reported.
	Plan *string
	// OrganizationID is the UUID reported by the native status command.
	OrganizationID string
	// AccountID is the UUID in native OAuth metadata matching the current status.
	AccountID string
	// ConfigDirectory is the native configuration root when the CLI reports it.
	ConfigDirectory string
}

// ReadLoginStatus reads native identity without a version probe or session.
func (c *Client) ReadLoginStatus(ctx context.Context) (LoginStatus, error) {
	output, err := c.command(ctx, 5*time.Second, "auth", "status", "--json")
	status := decodeOptional[struct {
		// LoggedIn is absent when the command failed to report a status.
		LoggedIn *bool `json:"loggedIn"`
		// Method is the native sign-in method.
		Method string `json:"authMethod"`
		// Provider identifies the active API backend.
		Provider string `json:"apiProvider"`
		// Email and Plan describe the native subscription.
		Email *string `json:"email"`
		Plan  *string `json:"subscriptionType"`
		// OrganizationID is emitted for Claude.ai subscriptions.
		OrganizationID string `json:"orgId"`
		// ConfigDirectory is available on newer CLI builds.
		ConfigDirectory string `json:"configDirectory"`
	}](output)
	var exited commandExit
	if err != nil && !(errors.As(err, &exited) && exited == 1 && status != nil && status.LoggedIn != nil && !*status.LoggedIn) {
		return LoginStatus{}, err
	}
	if status == nil || status.LoggedIn == nil {
		return LoginStatus{}, errors.New("Claude returned unreadable account status")
	}
	result := LoginStatus{Authenticated: *status.LoggedIn, ConfigDirectory: status.ConfigDirectory}
	if !result.Authenticated {
		return result, nil
	}
	result.Kind = "unknown"
	switch {
	case status.Provider == "bedrock" || status.Provider == "vertex" || status.Provider == "foundry":
		result.Kind = status.Provider
	case status.Method == "claude.ai" || status.Method == "claudeai" || status.Method == "oauth":
		result.AuthMethod = status.Method
		result.Kind, result.Email, result.Plan = "subscription", status.Email, status.Plan
		if isUUID(status.OrganizationID) {
			result.OrganizationID = strings.ToLower(status.OrganizationID)
			if !c.accountUsesEnvironment() && status.Email != nil && *status.Email != "" {
				result.AccountID = c.subscriptionAccountID(result.OrganizationID, *status.Email, runtime.GOOS)
			}
		}
	case status.Method == "apiKey" || status.Method == "api_key" || status.Method == "anthropicConsole":
		result.Kind = "api_key"
		result.AuthMethod = status.Method
	case status.Method == "oauth_token":
		result.AuthMethod = status.Method
		// Claude Code 2.1.159 reports the same method for a gateway token
		// and a Claude subscription token. Only the frozen environment
		// distinguishes the credential source in this response.
		switch {
		case c.environment("ANTHROPIC_AUTH_TOKEN") != "" && c.environment("CLAUDE_CODE_OAUTH_TOKEN") == "":
			result.Kind = "api_key"
		case c.environment("CLAUDE_CODE_OAUTH_TOKEN") != "" && c.environment("ANTHROPIC_AUTH_TOKEN") == "":
			result.Kind, result.Email, result.Plan = "subscription", status.Email, status.Plan
		}
	}
	return result, nil
}

// subscriptionAccountID complements the native status's orgId with its
// persisted OAuth account UUID. CLI 2.1.159 and 2.1.309 use .claude.json;
// the older build first checks the account directory's legacy .config.json.
func (c *Client) subscriptionAccountID(orgID, email, platform string) string {
	config := c.environment("CLAUDE_CONFIG_DIR")
	home := config
	if config == "" {
		// Claude uses os.homedir() to find its system metadata. A shell's
		// HOME on Windows does not replace the native USERPROFILE root.
		key := "HOME"
		if platform == "windows" {
			key = "USERPROFILE"
		}
		home = c.environment(key)
		if home == "" {
			return ""
		}
		config = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(config) {
		config = filepath.Join(c.options.WorkingDirectory, config)
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(c.options.WorkingDirectory, home)
	}
	path := filepath.Join(config, ".config.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		path = filepath.Join(home, ".claude.json")
		info, err = os.Lstat(path)
	}
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := readBoundedFile(path, 16<<20)
	if err != nil {
		return ""
	}
	record := decodeOptional[struct {
		// OAuthAccount is native non-secret identity metadata.
		OAuthAccount struct {
			// AccountID is the user UUID written by the native OAuth login.
			AccountID string `json:"accountUuid"`
			// OrganizationID is the organization written by the same login.
			OrganizationID string `json:"organizationUuid"`
			// Email is compared with the current CLI status for consistency.
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}](data)
	if record == nil || !strings.EqualFold(record.OAuthAccount.OrganizationID, orgID) ||
		!strings.EqualFold(record.OAuthAccount.Email, email) || !isUUID(record.OAuthAccount.AccountID) {
		return ""
	}
	return strings.ToLower(record.OAuthAccount.AccountID)
}

// ReadAccount obtains the native account observation without opening a session.
func (c *Client) ReadAccount(ctx context.Context) (acpmeta.AccountInfo, error) {
	if err := c.ready(); err != nil {
		return acpmeta.AccountInfo{}, err
	}
	status, err := c.ReadLoginStatus(ctx)
	if err != nil {
		return acpmeta.AccountInfo{}, err
	}
	if !status.Authenticated {
		return acpmeta.AccountInfo{RequiresAuthentication: true}, nil
	}
	return acpmeta.AccountInfo{Account: &acpmeta.AccountIdentity{Kind: status.Kind, Label: status.Email, Plan: status.Plan}}, nil
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
