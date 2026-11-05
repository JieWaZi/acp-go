package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

var (
	versionPattern       = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(?:-\d{2}-\d{2}-\d{2})?-[0-9a-f]{7,40}$`)
	ErrQuotaCredential   = errors.New("Cursor quota requires a bound default login credential")
	ErrQuotaUnauthorized = errors.New("Cursor Dashboard rejected the selected credential")
)

// CredentialSource identifies the configured credential independently of native sign-in.
type CredentialSource string

const (
	DefaultLogin CredentialSource = "default_login"
	APIKey       CredentialSource = "api_key"
)

// CredentialSelection is the caller's stable binding for one native login.
type CredentialSelection struct {
	// Source distinguishes Cursor login and an externally selected API key.
	Source CredentialSource
	// ID is the host-owned opaque login identity.
	ID string
}

// DashboardCredential supplies a token for the host-selected login. Dashboard verifies its user.
type DashboardCredential struct {
	// Selection must match the requested host binding.
	Selection CredentialSelection
	// AccessToken is scoped to the configured Dashboard origin.
	AccessToken string
}

// Dashboard is the optional credential-bound Cursor quota reader.
type Dashboard struct {
	// Endpoint is the explicitly trusted Dashboard HTTPS or loopback endpoint.
	// An empty value uses Cursor's default independently of the CLI environment.
	Endpoint string
	// ExpectedUserID is the verified native status user required for quota reads.
	ExpectedUserID string
	// HTTPClient provides optional caller-owned transport.
	HTTPClient *http.Client
	// Credential resolves exactly the requested login without ambient fallback.
	Credential func(context.Context, CredentialSelection) (DashboardCredential, error)
}

func (c *Client) AuthenticationSource() CredentialSource {
	if c.apiKey != "" {
		return APIKey
	}
	return DefaultLogin
}

func parseVersion(executable string, output []byte) string {
	version := strings.TrimSpace(string(output))
	if versionPattern.MatchString(version) {
		return version
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		return manifestVersion(executable)
	}
	return ""
}

// manifestVersion handles the Windows installer layout when its launcher does
// not yet produce a version on stdout.
func manifestVersion(executable string) string {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil || cliName(real) != "cursor-agent" {
		return ""
	}
	ext, bin := strings.ToLower(filepath.Ext(real)), filepath.Dir(real)
	if (ext != ".cmd" && ext != ".bat") || filepath.Base(bin) != "bin" {
		return ""
	}
	file, err := os.Open(filepath.Join(filepath.Dir(bin), "cursor-package.json"))
	if err != nil {
		return ""
	}
	defer file.Close()
	var manifest struct {
		// Version is the native installer build.
		Version string `json:"version"`
		// Target verifies this is the supported launcher layout.
		Target string `json:"target"`
		// Executable must identify the selected launcher.
		Executable string `json:"executable"`
	}
	const limit = 32 << 10
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &manifest) != nil ||
		manifest.Target != "win32-x64" || manifest.Executable != "bin/"+filepath.Base(real) {
		return ""
	}
	if version := strings.TrimSpace(manifest.Version); versionPattern.MatchString(version) {
		return version
	}
	return ""
}

func cliName(path string) string {
	name := strings.ToLower(filepath.Base(path))
	switch filepath.Ext(name) {
	case ".exe", ".cmd", ".bat":
		return strings.TrimSuffix(name, filepath.Ext(name))
	default:
		return name
	}
}

func (c *Client) runCommandTo(ctx context.Context, env []string, command string, limit int64, output io.Writer, args ...string) (result error) {
	argv := append(slices.Clone(c.opts.PrefixArgs), command)
	proc, err := agentclient.NewProcess(agentclient.ProcessSpec{
		Executable: c.opts.Executable, Args: append(argv, args...), Env: env, Dir: c.opts.WorkingDirectory,
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed || c.command != nil {
		c.mu.Unlock()
		return ErrRestartRequired
	}
	c.command = proc
	c.mu.Unlock()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.closeCommand(cleanup, proc); err != nil {
			result = errors.Join(result, err)
		}
	}()
	if err := proc.Start(ctx); err != nil {
		return err
	}
	_ = proc.Stdin().Close()
	stop := context.AfterFunc(ctx, func() { _ = proc.Stdout().Close() })
	defer stop()
	n, err := io.Copy(output, io.LimitReader(proc.Stdout(), limit+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || n > limit {
		return errors.New("Cursor CLI output delivery failed")
	}
	exit, err := proc.Wait(ctx)
	if err != nil {
		return err
	}
	if exit.Code != 0 || exit.Err != nil {
		return fmt.Errorf("Cursor %s exited with code %d", command, exit.Code)
	}
	return nil
}

func (c *Client) closeCommand(ctx context.Context, proc *agentclient.Process) error {
	if err := proc.Close(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	if c.command == proc {
		c.command = nil
	}
	c.mu.Unlock()
	return nil
}

// LoginLink runs the Cursor CLI's headless login flow and returns its native output.
func (c *Client) LoginLink(ctx context.Context, output io.Writer) error {
	if output == nil || c.apiKey != "" {
		return invalid("Cursor headless login requires a default login and an output writer")
	}
	if err := c.RetireAuthentication(ctx); err != nil {
		return err
	}
	op, cancel := c.managementContext(ctx, 5*time.Minute)
	defer cancel()
	env := append(slices.Clone(c.opts.Environment), "NO_OPEN_BROWSER=1")
	return c.runCommandTo(op, env, "login", 1<<20, output)
}

func (c *Client) managementContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	op, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(c.management, cancel)
	return op, func() { stop(); cancel() }
}

// LoginStatus describes the selected native sign-in independently of plan usage.
type LoginStatus struct {
	// Authenticated is the local login status reported by the CLI.
	Authenticated bool
	// UserID is the provider's account identity when the CLI reports it.
	UserID string
	// Email is the optional provider label.
	Email string
}

// ReadLoginStatus reads Cursor's local sign-in without starting an ACP session.
// The CLI status command describes the stored login even when an API key is selected.
func (c *Client) ReadLoginStatus(ctx context.Context) (LoginStatus, error) {
	if c.apiKey != "" {
		return LoginStatus{}, acp.NewMethodNotFound(acpmeta.AccountReadMethod)
	}
	op, cancel := c.managementContext(ctx, 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	err := c.runCommandTo(op, c.opts.Environment, "status", 64<<10, &output, "--format", "json")
	if ctx.Err() != nil {
		return LoginStatus{}, ctx.Err()
	}
	if c.management.Err() != nil {
		return LoginStatus{}, ErrRestartRequired
	}
	if err != nil {
		return LoginStatus{}, err
	}
	var status struct {
		// Authenticated is required to distinguish an absent field from a signed-out account.
		Authenticated *bool `json:"isAuthenticated"`
		// UserInfo identifies a locally signed-in account when available.
		UserInfo struct {
			// UserID is the native GetMeResponse user_id, serialized as an int32.
			UserID json.Number `json:"userId"`
			// Email is the native label displayed by account consumers.
			Email string `json:"email"`
		} `json:"userInfo"`
	}
	if json.Unmarshal(output.Bytes(), &status) != nil || status.Authenticated == nil {
		return LoginStatus{}, errors.New("Cursor CLI account status is invalid")
	}
	if !*status.Authenticated {
		return LoginStatus{}, nil
	}
	return LoginStatus{
		Authenticated: true,
		UserID:        cursorUserID(status.UserInfo.UserID),
		Email:         strings.TrimSpace(status.UserInfo.Email),
	}, nil
}

// ReadAccount projects native status independently of the quota reader.
func (c *Client) ReadAccount(ctx context.Context) (acpmeta.AccountInfo, error) {
	if err := ctx.Err(); err != nil {
		return acpmeta.AccountInfo{}, err
	}
	if c.management.Err() != nil {
		return acpmeta.AccountInfo{}, ErrRestartRequired
	}
	if c.AuthenticationSource() == APIKey {
		// This describes the frozen launch credential. Only a native provider
		// operation can verify the key, and local sign-in says nothing about it.
		return acpmeta.AccountInfo{Account: &acpmeta.AccountIdentity{Kind: string(APIKey)}}, nil
	}
	status, err := c.ReadLoginStatus(ctx)
	if err != nil {
		return acpmeta.AccountInfo{}, err
	}
	if !status.Authenticated {
		return acpmeta.AccountInfo{RequiresAuthentication: true}, nil
	}
	account := &acpmeta.AccountIdentity{Kind: string(DefaultLogin)}
	if status.Email != "" {
		account.Label = &status.Email
	}
	return acpmeta.AccountInfo{Account: account}, nil
}

func (c *Client) ReadAccountUsage(ctx context.Context) (acpmeta.AccountUsage, error) {
	if c.opts.Dashboard == nil || c.apiKey != "" {
		return acpmeta.AccountUsage{}, acp.NewMethodNotFound(acpmeta.AccountUsageMethod)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return acpmeta.AccountUsage{}, ErrRestartRequired
	}
	if c.quotaReaders == 0 {
		c.quotaIdle = make(chan struct{})
	}
	c.quotaReaders++
	c.mu.Unlock()
	defer c.finishQuotaRead()

	op, cancel := c.managementContext(ctx, 10*time.Second)
	defer cancel()
	usage, err := c.opts.Dashboard.read(op, c.opts.Credential)
	if ctx.Err() != nil {
		return acpmeta.AccountUsage{}, ctx.Err()
	}
	if c.management.Err() != nil {
		return acpmeta.AccountUsage{}, ErrRestartRequired
	}
	return usage, err
}

func (c *Client) finishQuotaRead() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quotaReaders--
	if c.quotaReaders == 0 {
		close(c.quotaIdle)
		c.quotaIdle = nil
	}
}

func (d Dashboard) read(ctx context.Context, selection CredentialSelection) (acpmeta.AccountUsage, error) {
	var usage acpmeta.AccountUsage
	expectedUser := cursorUserID(json.Number(d.ExpectedUserID))
	if selection.Source != DefaultLogin || selection.ID == "" || d.Credential == nil || expectedUser == "" {
		return usage, ErrQuotaCredential
	}
	endpoint := strings.TrimRight(strings.TrimSpace(d.Endpoint), "/")
	if endpoint == "" {
		endpoint = "https://api2.cursor.sh"
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return usage, errors.New("invalid Cursor Dashboard endpoint")
	}
	ip := net.ParseIP(origin.Hostname())
	if origin.Scheme != "https" && !(origin.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return usage, errors.New("invalid Cursor Dashboard transport")
	}
	credential, err := d.Credential(ctx, selection)
	if ctx.Err() != nil {
		return usage, ctx.Err()
	}
	if err != nil || credential.Selection != selection || strings.TrimSpace(credential.AccessToken) == "" || strings.ContainsAny(credential.AccessToken, "\r\n") {
		return usage, ErrQuotaCredential
	}
	client := d.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	post := func(method string, result any) error {
		path := endpoint + "/aiserver.v1.DashboardService/" + method
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader("{}"))
		if err != nil {
			return errors.New("invalid Cursor Dashboard request")
		}
		request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Connect-Protocol-Version", "1")
		response, err := noRedirect.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("Cursor Dashboard transport failed")
		}
		defer response.Body.Close()
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return ErrQuotaUnauthorized
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return fmt.Errorf("Cursor Dashboard returned HTTP %d", response.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 || !strings.HasPrefix(strings.TrimSpace(string(body)), "{") || json.Unmarshal(body, result) != nil {
			return errors.New("Cursor Dashboard response is invalid")
		}
		return nil
	}
	// Native status copies GetMeResponse.userId. Verify that same identity
	// with the exact bearer that will read the quota, even on the first use.
	var identity struct {
		// UserID identifies the bearer account independently of its label or plan.
		UserID json.Number `json:"userId"`
	}
	if err := post("GetMe", &identity); err != nil {
		return usage, err
	}
	if cursorUserID(identity.UserID) != expectedUser {
		return usage, ErrQuotaCredential
	}
	var period struct {
		// Start and End are Connect JSON int64 values, in milliseconds.
		Start json.RawMessage `json:"billingCycleStart"`
		End   json.RawMessage `json:"billingCycleEnd"`
		// Usage includes only the percentage slots that the provider returned.
		Usage struct {
			// Auto reports the Cursor-model share.
			Auto *float64 `json:"autoPercentUsed"`
			// API reports the external-model share.
			API *float64 `json:"apiPercentUsed"`
			// Total is authoritative for merged plans.
			Total *float64 `json:"totalPercentUsed"`
		} `json:"planUsage"`
	}
	if err := post("GetCurrentPeriodUsage", &period); err != nil {
		return usage, err
	}
	var plan struct {
		// Merged chooses one combined usage slot when true.
		Merged bool `json:"isMerged"`
	}
	_ = post("GetPlanInfo", &plan)
	var start, end = dashboardMillis(period.Start), dashboardMillis(period.End)
	var reset, duration *int64
	if end != nil {
		seconds := *end / 1000
		reset = &seconds
		if start != nil && *end > *start {
			elapsed := (*end - *start) / 1000
			duration = &elapsed
		}
	}
	var windows []acpmeta.AccountUsageWindow
	add := func(kind string, value *float64) {
		if value != nil && *value >= 0 {
			windows = append(windows, acpmeta.AccountUsageWindow{Kind: kind, UsedPercent: value, DurationSeconds: duration, ResetAtUnixSeconds: reset})
		}
	}
	if plan.Merged || period.Usage.Auto == nil && period.Usage.API == nil {
		add("primary", period.Usage.Total)
	} else {
		add("primary", period.Usage.Auto)
		add("secondary", period.Usage.API)
	}
	if len(windows) > 0 {
		usage.Buckets = []acpmeta.AccountUsageBucket{{ID: "cursor", Windows: windows}}
	}
	return usage, nil
}

func cursorUserID(value json.Number) string {
	id, err := strconv.ParseUint(string(value), 10, 31)
	if err != nil || id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 10)
}

func dashboardMillis(raw json.RawMessage) *int64 {
	value, err := strconv.ParseInt(strings.Trim(string(raw), `"`), 10, 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}
