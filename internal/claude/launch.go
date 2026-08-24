package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

var (
	// ErrInvalidWorkingDirectory 表示 Session 工作目录不存在、不是目录或不是绝对路径。
	ErrInvalidWorkingDirectory = errors.New("invalid Claude working directory")
	// ErrInvalidAdditionalDirectory 表示附加目录无法规范化为可访问目录。
	ErrInvalidAdditionalDirectory = errors.New("invalid Claude additional directory")
	// ErrInvalidMCPServer 表示客户端 MCP 定义缺少必填字段或包含不支持的传输。
	ErrInvalidMCPServer = errors.New("invalid Claude MCP server")
)

// launchOptions 保存启动一个 Session 进程所需的稳定参数。
type launchOptions struct {
	// CWD 是 CLI 工作目录。
	CWD string
	// SessionID 是预先分配或待恢复的持久会话标识。
	SessionID string
	// Resume 表示恢复已有会话而不是创建新会话。
	Resume bool
	// AdditionalDirectories 是已经规范化并去重的附加目录。
	AdditionalDirectories []string
	// MCPConfig 是传给 CLI 的 MCP JSON；无 server 时为空。
	MCPConfig string
	// PermissionMode 是进程启动时使用的权限模式。
	PermissionMode string
}

// mcpConfigEnvelope 是 CLI 参数接收的 MCP 配置根对象。
type mcpConfigEnvelope struct {
	// MCPServers 按名称保存不同传输的 server 配置。
	MCPServers map[string]any `json:"mcpServers"`
}

// prepareLaunchOptions 在创建任何子进程前验证 Session 定义参数。
func prepareLaunchOptions(
	cwd string,
	sessionID string,
	resume bool,
	additionalDirectories []string,
	mcpServers []acp.McpServer,
) (launchOptions, error) {
	if err := validateWorkingDirectory(cwd); err != nil {
		return launchOptions{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return launchOptions{}, errors.New("preparing Claude launch: session id is empty")
	}
	directories, err := normalizeAdditionalDirectories(cwd, additionalDirectories)
	if err != nil {
		return launchOptions{}, err
	}
	mcpConfig, err := encodeMCPConfig(mcpServers)
	if err != nil {
		return launchOptions{}, err
	}
	return launchOptions{
		CWD:                   cwd,
		SessionID:             sessionID,
		Resume:                resume,
		AdditionalDirectories: directories,
		MCPConfig:             mcpConfig,
		PermissionMode:        "default",
	}, nil
}

// validateWorkingDirectory 保证 cwd 是本机存在的绝对目录。
func validateWorkingDirectory(cwd string) error {
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("validating cwd %q: %w: path is not absolute", cwd, ErrInvalidWorkingDirectory)
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return fmt.Errorf("validating cwd %q: %w: %v", cwd, ErrInvalidWorkingDirectory, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("validating cwd %q: %w: path is not a directory", cwd, ErrInvalidWorkingDirectory)
	}
	return nil
}

// normalizeAdditionalDirectories 按 cwd 解析相对路径，并返回排序稳定的唯一绝对目录。
func normalizeAdditionalDirectories(cwd string, directories []string) ([]string, error) {
	unique := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		if strings.TrimSpace(directory) == "" {
			return nil, fmt.Errorf("normalizing additional directory: %w: path is empty", ErrInvalidAdditionalDirectory)
		}
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(cwd, directory)
		}
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return nil, fmt.Errorf("normalizing additional directory: %w: %v", ErrInvalidAdditionalDirectory, err)
		}
		absolute = filepath.Clean(absolute)
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, fmt.Errorf("normalizing additional directory %q: %w: %v", absolute, ErrInvalidAdditionalDirectory, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("normalizing additional directory %q: %w: path is not a directory", absolute, ErrInvalidAdditionalDirectory)
		}
		unique[absolute] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for directory := range unique {
		result = append(result, directory)
	}
	// 稳定排序使相同请求总是生成相同参数和 Session 指纹。
	sort.Strings(result)
	return result, nil
}

// encodeMCPConfig 把 ACP MCP union 转成 CLI 接受的命名配置对象。
func encodeMCPConfig(servers []acp.McpServer) (string, error) {
	if len(servers) == 0 {
		return "", nil
	}
	configured := make(map[string]any, len(servers))
	for _, server := range servers {
		name, value, err := convertMCPServer(server)
		if err != nil {
			return "", err
		}
		if _, exists := configured[name]; exists {
			return "", fmt.Errorf("encoding MCP server %q: %w: duplicate name", name, ErrInvalidMCPServer)
		}
		configured[name] = value
	}
	data, err := json.Marshal(mcpConfigEnvelope{MCPServers: configured})
	if err != nil {
		return "", fmt.Errorf("encoding MCP config: %w", err)
	}
	return string(data), nil
}

// convertMCPServer 校验 union 恰有一个变体，并返回名称与 wire 配置。
func convertMCPServer(server acp.McpServer) (string, map[string]any, error) {
	variants := 0
	if server.Stdio != nil {
		variants++
	}
	if server.Http != nil {
		variants++
	}
	if server.Sse != nil {
		variants++
	}
	if server.Acp != nil {
		variants++
	}
	if variants != 1 {
		return "", nil, fmt.Errorf("encoding MCP server: %w: expected exactly one transport", ErrInvalidMCPServer)
	}

	switch {
	case server.Stdio != nil:
		value := server.Stdio
		if strings.TrimSpace(value.Name) == "" || strings.TrimSpace(value.Command) == "" {
			return "", nil, fmt.Errorf("encoding stdio MCP server: %w: name and command are required", ErrInvalidMCPServer)
		}
		env, err := environmentVariables(value.Env)
		if err != nil {
			return "", nil, fmt.Errorf("encoding stdio MCP server %q: %w", value.Name, err)
		}
		return value.Name, map[string]any{
			"type": "stdio", "command": value.Command, "args": value.Args, "env": env,
		}, nil
	case server.Http != nil:
		return convertRemoteMCPServer("http", server.Http.Name, server.Http.Url, server.Http.Headers)
	case server.Sse != nil:
		return convertRemoteMCPServer("sse", server.Sse.Name, server.Sse.Url, server.Sse.Headers)
	default:
		return "", nil, fmt.Errorf("encoding ACP-transport MCP server: %w: unsupported transport", ErrInvalidMCPServer)
	}
}

// convertRemoteMCPServer 校验 HTTP/SSE 必填字段并保留 header 值。
func convertRemoteMCPServer(kind, name, rawURL string, headers []acp.HttpHeader) (string, map[string]any, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(rawURL) == "" {
		return "", nil, fmt.Errorf("encoding %s MCP server: %w: name and url are required", kind, ErrInvalidMCPServer)
	}
	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return "", nil, fmt.Errorf("encoding %s MCP server %q: %w: url must be absolute HTTP(S)", kind, name, ErrInvalidMCPServer)
	}
	headerValues := make(map[string]string, len(headers))
	for _, header := range headers {
		canonicalName := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(header.Name))
		if canonicalName == "" {
			return "", nil, fmt.Errorf("encoding %s MCP server %q: %w: header name is empty", kind, name, ErrInvalidMCPServer)
		}
		if _, exists := headerValues[canonicalName]; exists {
			return "", nil, fmt.Errorf("encoding %s MCP server %q: %w: duplicate header", kind, name, ErrInvalidMCPServer)
		}
		headerValues[canonicalName] = header.Value
	}
	return name, map[string]any{"type": kind, "url": rawURL, "headers": headerValues}, nil
}

// environmentVariables 把 ACP 键值列表转换为 CLI 配置对象，并拒绝重复键。
func environmentVariables(values []acp.EnvVariable) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.Name) == "" {
			return nil, fmt.Errorf("%w: environment variable name is empty", ErrInvalidMCPServer)
		}
		if _, exists := result[value.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate environment variable", ErrInvalidMCPServer)
		}
		result[value.Name] = value.Value
	}
	return result, nil
}

// claudeLaunchArgs 按稳定顺序构造 Session CLI 参数。
func claudeLaunchArgs(options launchOptions) []string {
	args := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--input-format", "stream-json",
		"--permission-prompt-tool", "stdio",
		"--include-partial-messages",
		"--replay-user-messages",
		"--setting-sources=user,project,local",
	}
	if options.Resume {
		args = append(args, "--resume="+options.SessionID)
	} else {
		args = append(args, "--session-id="+options.SessionID)
	}
	if options.PermissionMode != "" {
		args = append(args, "--permission-mode", options.PermissionMode)
	}
	if options.MCPConfig != "" {
		args = append(args, "--mcp-config", options.MCPConfig)
	}
	for _, directory := range options.AdditionalDirectories {
		args = append(args, "--add-dir", directory)
	}
	return args
}

// launchFingerprint 标识会影响长期 Session 进程定义的参数。
func launchFingerprint(options launchOptions) string {
	data, _ := json.Marshal(options)
	return string(data)
}
