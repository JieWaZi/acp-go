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
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
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

// DashboardCredential identifies the exact verified native login and token.
type DashboardCredential struct {
	// Selection must match the requested host binding.
	Selection CredentialSelection
	// AccessToken is scoped to the configured Dashboard origin.
	AccessToken string
}

// Dashboard is the optional credential-bound Cursor quota reader.
type Dashboard struct {
	// Endpoint is the native Dashboard HTTPS endpoint or a loopback test endpoint.
	Endpoint string
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

func (c *Client) probeVersion(ctx context.Context) (string, error) {
	versionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	data, err := c.runCommand(versionCtx, c.opts.Environment, "--version", 4096)
	version := strings.TrimSpace(string(data))
	if err == nil && versionPattern.MatchString(version) {
		return version, nil
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" && versionCtx.Err() == nil {
		if version := manifestVersion(c.opts.Executable); version != "" {
			return version, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("selected executable did not report a Cursor CLI version")
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

func (c *Client) runCommand(ctx context.Context, env []string, command string, limit int64) ([]byte, error) {
	var output bytes.Buffer
	err := c.runCommandTo(ctx, env, command, limit, &output)
	return output.Bytes(), err
}

func (c *Client) runCommandTo(ctx context.Context, env []string, command string, limit int64, output io.Writer) (result error) {
	proc, err := agentclient.NewProcess(agentclient.ProcessSpec{
		Executable: c.opts.Executable, Args: append(slices.Clone(c.opts.PrefixArgs), command), Env: env, Dir: c.opts.WorkingDirectory,
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
		cleanup, cancel := context.WithTimeout(context.Background(), controlTimeout)
		defer cancel()
		if err := c.closeCommand(cleanup, proc); err != nil {
			c.invalidate()
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

func (c *Client) Authenticate(ctx context.Context, request acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.AuthenticateResponse{}, err
	}
	defer c.end()
	if request.MethodId != loginMethod || c.apiKey != "" {
		return acp.AuthenticateResponse{}, invalid("Cursor native login is unavailable for this selection")
	}
	advertised := slices.ContainsFunc(c.initialized.AuthMethods, func(method acp.AuthMethod) bool {
		return method.Agent != nil && method.Agent.Id == loginMethod
	})
	if !advertised {
		return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
	}
	op, cancel := c.operationContext(ctx, 5*time.Minute)
	defer cancel()
	r := send[acp.AuthenticateResponse](conn, op, acp.AgentMethodAuthenticate, request)
	return r.Result, receiptFailure(r, acp.AgentMethodAuthenticate)
}

// LoginLink runs the Cursor CLI's headless login flow and returns its native output.
func (c *Client) LoginLink(ctx context.Context, output io.Writer) error {
	if output == nil || c.apiKey != "" {
		return invalid("Cursor headless login requires a default login and an output writer")
	}
	if _, err := c.begin(ctx); err != nil {
		return err
	}
	defer c.end()
	defer c.invalidate()
	op, cancel := c.operationContext(ctx, 5*time.Minute)
	defer cancel()
	env := append(slices.Clone(c.opts.Environment), "NO_OPEN_BROWSER=1")
	return c.runCommandTo(op, env, "login", 1<<20, output)
}

func (c *Client) Logout(ctx context.Context, request acp.LogoutRequest) (acp.LogoutResponse, error) {
	conn, err := c.begin(ctx)
	if err != nil {
		return acp.LogoutResponse{}, err
	}
	defer c.end()
	if c.apiKey != "" {
		return acp.LogoutResponse{}, invalid("Cursor API key selection does not support logout")
	}
	// A native process can keep the former credential cached after logout.
	defer c.invalidate()
	op, cancel := c.operationContext(ctx, requestTimeout)
	defer cancel()
	if c.initialized.AgentCapabilities.Auth.Logout == nil {
		_, err := c.runCommand(op, c.opts.Environment, "logout", 64<<10)
		return acp.LogoutResponse{}, err
	}
	r := send[acp.LogoutResponse](conn, op, acp.AgentMethodLogout, request)
	return r.Result, receiptFailure(r, acp.AgentMethodLogout)
}

func (c *Client) invalidate() {
	c.mu.Lock()
	c.unusable = true
	c.mu.Unlock()
}

func (c *Client) ReadAccount(ctx context.Context) (acpmeta.AccountInfo, error) {
	if c.opts.Dashboard == nil || c.apiKey != "" {
		return acpmeta.AccountInfo{}, acp.NewMethodNotFound(acpmeta.AccountReadMethod)
	}
	if _, err := c.ReadAccountUsage(ctx); err != nil {
		if errors.Is(err, ErrQuotaUnauthorized) {
			return acpmeta.AccountInfo{RequiresAuthentication: true}, nil
		}
		return acpmeta.AccountInfo{}, err
	}
	return acpmeta.AccountInfo{Account: &acpmeta.AccountIdentity{Kind: string(DefaultLogin)}}, nil
}

func (c *Client) ReadAccountUsage(ctx context.Context) (acpmeta.AccountUsage, error) {
	if c.opts.Dashboard == nil || c.apiKey != "" {
		return acpmeta.AccountUsage{}, acp.NewMethodNotFound(acpmeta.AccountUsageMethod)
	}
	if _, err := c.begin(ctx); err != nil {
		return acpmeta.AccountUsage{}, err
	}
	defer c.end()
	op, cancel := c.operationContext(ctx, 10*time.Second)
	defer cancel()
	usage, err := c.opts.Dashboard.read(op, c.opts.Credential)
	if ctx.Err() != nil {
		return acpmeta.AccountUsage{}, ctx.Err()
	}
	return usage, err
}

func (d Dashboard) read(ctx context.Context, selection CredentialSelection) (acpmeta.AccountUsage, error) {
	var usage acpmeta.AccountUsage
	if selection.Source != DefaultLogin || selection.ID == "" || d.Credential == nil {
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

func dashboardMillis(raw json.RawMessage) *int64 {
	value, err := strconv.ParseInt(strings.Trim(string(raw), `"`), 10, 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}
