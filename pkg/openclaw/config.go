package openclaw

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

const (
	gatewayURLVariable  = "OPENCLAW_GATEWAY_URL"
	gatewayPortVariable = "OPENCLAW_GATEWAY_PORT"
	configPathVariable  = "OPENCLAW_CONFIG_PATH"
	includeRootVariable = "OPENCLAW_INCLUDE_ROOTS"
)

var profileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func loopbackGateway(target string) (string, error) {
	endpoint, err := url.Parse(target)
	if err != nil || endpoint.Scheme != "ws" && endpoint.Scheme != "wss" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery ||
		strings.Contains(target, "#") || endpoint.Path != "" && endpoint.Path != "/" {
		return "", errors.New("OpenClaw Gateway requires a loopback WebSocket URL")
	}
	ip := net.ParseIP(endpoint.Hostname())
	port := tcpPort(endpoint.Port())
	if ip == nil || !ip.IsLoopback() || port == 0 {
		return "", errors.New("OpenClaw Gateway requires a literal loopback address and port")
	}
	return endpoint.Scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

func envValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(env[i], "=")
		if ok && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func setEnv(env []string, name, value string) []string {
	return append(slices.DeleteFunc(env, func(record string) bool {
		key, _, _ := strings.Cut(record, "=")
		return strings.EqualFold(key, name)
	}), name+"="+value)
}

func localGatewayEnvironment(env []string) ([]string, error) {
	target := strings.TrimSpace(envValue(env, gatewayURLVariable))
	if target != "" {
		var err error
		target, err = loopbackGateway(target)
		if err != nil {
			return nil, err
		}
	}
	env = setEnv(slices.Clone(env), gatewayURLVariable, target)
	env = setEnv(env, gatewayPortVariable, strings.TrimSpace(envValue(env, gatewayPortVariable)))
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NODE_USE_ENV_PROXY"} {
		env = setEnv(env, name, "")
	}
	return setEnv(env, "NO_PROXY", "*"), nil
}

type nativeGatewayConfig struct {
	// exists records whether the native config file can be included.
	exists bool
	// Mode distinguishes OpenClaw's local Gateway from configured remote access.
	Mode string `json:"mode"`
	// Port is the native configured local listener before environment overrides.
	Port int `json:"port"`
	// TLS provides the native local transport mode. Certificate selection stays with the CLI.
	TLS struct {
		// Enabled selects native WebSocket TLS.
		Enabled bool `json:"enabled"`
	} `json:"tls"`
}

func (g *gateway) readProfile(ctx context.Context) (string, nativeGatewayConfig, error) {
	var config nativeGatewayConfig
	var selected struct {
		// Path is the active config selected by the native CLI and its profile flags.
		Path string `json:"path"`
	}
	output, code, err := g.run(ctx, g.process.Env, "config", "file", "--json")
	if err != nil {
		return "", config, err
	}
	if code != 0 || json.Unmarshal(output, &selected) != nil || !filepath.IsAbs(selected.Path) {
		return "", config, errors.New("cannot resolve the active OpenClaw configuration path")
	}
	info, err := os.Stat(selected.Path)
	if errors.Is(err, os.ErrNotExist) {
		return selected.Path, config, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", config, errors.New("cannot read the active OpenClaw configuration")
	}
	config.exists = true
	output, err = g.profileSection(ctx, "gateway")
	if err != nil {
		return "", config, err
	}
	if len(output) == 0 {
		return selected.Path, config, nil
	}
	if json.Unmarshal(output, &config) != nil {
		return "", config, errors.New("cannot read the OpenClaw Gateway configuration")
	}
	if config.Mode != "" && config.Mode != "local" {
		return "", config, errors.New("OpenClaw requires a local Gateway profile")
	}
	return selected.Path, config, nil
}

func (g *gateway) profileSection(ctx context.Context, section string) ([]byte, error) {
	output, code, err := g.run(ctx, g.process.Env, "config", "get", section, "--json")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		var report struct {
			// Error describes the native config read failure.
			Error struct {
				// Message identifies the valid but unauthored Gateway config case.
				Message string `json:"message"`
			} `json:"error"`
		}
		if code == 1 && json.Unmarshal(output, &report) == nil &&
			strings.HasPrefix(report.Error.Message, "Config path is valid but unset: "+section+".") {
			return nil, nil
		}
		return nil, errors.New("cannot verify the OpenClaw profile configuration")
	}
	return output, nil
}

func tcpPort(value string) int {
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0
		}
	}
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0
	}
	return int(port)
}

// configuredGatewayPort follows the native profile and environment precedence.
func configuredGatewayPort(spec agentclient.ProcessSpec, configured int) (int, error) {
	profile := strings.TrimSpace(envValue(spec.Env, "OPENCLAW_PROFILE"))
	profileFlag := false
	for i, arg := range spec.Args {
		flag, value, hasValue := strings.Cut(arg, "=")
		switch flag {
		case "--dev":
			profile = "dev"
			profileFlag = true
		case "--profile":
			if !hasValue && i+1 < len(spec.Args) {
				value = spec.Args[i+1]
			}
			profile = value
			profileFlag = true
		}
	}
	value := envValue(spec.Env, gatewayPortVariable)
	if value != "" {
		port := tcpPort(value)
		if _, suffix, err := net.SplitHostPort(value); err == nil {
			port = tcpPort(suffix)
		}
		if port == 0 {
			return 0, errors.New("OpenClaw Gateway port override is invalid")
		}
		return port, nil
	}
	if profileFlag && profile == "dev" {
		return 19001, nil
	}
	if configured > 0 && configured <= 65535 {
		return configured, nil
	}
	if profile == "" || strings.EqualFold(profile, "default") {
		return 18789, nil
	}
	if !profileName.MatchString(profile) {
		return 0, errors.New("OpenClaw Gateway profile name is invalid")
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(profile))
	return 20000 + int(h.Sum32()%40000), nil
}

func (g *gateway) bridgeProfile(ctx context.Context) (bridge agentclient.ProcessSpec, pinDir string, err error) {
	spec := g.process
	var empty agentclient.ProcessSpec
	original, cfg, err := g.readProfile(ctx)
	if err != nil {
		return empty, "", err
	}
	port, err := configuredGatewayPort(spec, cfg.Port)
	if err != nil {
		return empty, "", err
	}
	g.url = envValue(spec.Env, gatewayURLVariable)
	if g.url == "" {
		scheme := "ws"
		if cfg.TLS.Enabled {
			scheme = "wss"
		}
		g.url = scheme + "://127.0.0.1:" + strconv.Itoa(port)
	}
	// Keep an implicit URL implicit so OpenClaw selects and resolves config
	// credentials and the local TLS pin. Its ACP CLI lacks --expect-url, so
	// pin routing fields in a private include and freeze which environment
	// overrides the native loader may publish before opening its connection.
	privateDir, err := os.MkdirTemp("", "acp-openclaw-")
	if err != nil {
		return empty, "", errors.New("cannot create a private OpenClaw ACP configuration")
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(privateDir)
		}
	}()
	overlay := map[string]any{"gateway": map[string]any{
		"mode": "local", "port": port, "tls": map[string]bool{"enabled": cfg.TLS.Enabled},
	}}
	spec.Env = slices.Clone(spec.Env)
	if cfg.exists {
		profileEnv, err := g.profileEnvironment(ctx, original)
		if err != nil {
			return empty, "", err
		}
		// Native includes merge object fields, so first replace the inherited
		// env by a scalar, then restore the filtered map. Late edits to the
		// source (including mixed-case and whitespace aliases) cannot inject a
		// routing override during the same native config read.
		const resetEnvironment = "reset-env.json"
		if err := os.WriteFile(filepath.Join(privateDir, resetEnvironment), []byte(`{"env":null}`), 0600); err != nil {
			return empty, "", errors.New("cannot write the pinned OpenClaw profile environment")
		}
		if profileEnv == nil {
			profileEnv = make(map[string]json.RawMessage)
		}
		overlay["env"] = profileEnv
		overlay["$include"] = []string{original, resetEnvironment}
		roots := filepath.SplitList(envValue(spec.Env, includeRootVariable))
		roots = append(roots, filepath.Dir(original))
		spec.Env = setEnv(spec.Env, includeRootVariable, strings.Join(roots, string(os.PathListSeparator)))
	}
	if strings.TrimSpace(envValue(spec.Env, "OPENCLAW_STATE_DIR")) == "" {
		// The CLI normally reads .env beside the explicit config or from its
		// default state directory. Preserve that source without copying secrets.
		profileEnv := filepath.Join(filepath.Dir(original), ".env")
		info, statErr := os.Stat(profileEnv)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) || statErr == nil && info.Mode().IsRegular() &&
			os.Symlink(profileEnv, filepath.Join(privateDir, ".env")) != nil {
			return empty, "", errors.New("cannot preserve the native OpenClaw profile environment")
		}
	}
	path := filepath.Join(privateDir, "openclaw.json")
	data, _ := json.Marshal(overlay)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return empty, "", errors.New("cannot write the pinned OpenClaw ACP configuration")
	}
	spec.Env = setEnv(spec.Env, configPathVariable, path)
	spec.Env = setEnv(spec.Env, gatewayPortVariable, strconv.Itoa(port))
	g.pinnedEnv = spec.Env
	return spec, privateDir, nil
}
