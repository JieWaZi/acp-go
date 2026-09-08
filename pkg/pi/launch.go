package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// resolveDependencies 复用同一 npm 安装中的 Pi 与 MCP 库，不在发现阶段下载软件。
func resolveDependencies(adapter string, config Config) (string, string, error) {
	piPath := config.PiPath
	if piPath == "" {
		piPath = nativeacp.EnvironmentValue(config.Environment, "PI_ACP_PI_COMMAND")
	}
	if piPath == "" {
		name := "pi"
		if runtime.GOOS == "windows" {
			name = "pi.cmd"
		}
		candidate := filepath.Join(filepath.Dir(adapter), name)
		if _, err := os.Stat(candidate); err == nil {
			piPath = candidate
		} else {
			piPath = name
		}
	}
	piPath, err := nativeacp.ResolveCommand(piPath, config.Environment)
	if err != nil {
		return "", "", fmt.Errorf("Pi executable required: %w", err)
	}
	module := config.MCPModulePath
	if module == "" {
		module = nativeacp.EnvironmentValue(config.Environment, "PI_MCP_ADAPTER_PATH")
	}
	if module == "" {
		resolved, err := filepath.EvalSymlinks(adapter)
		if err != nil {
			return "", "", err
		}
		for directory := filepath.Dir(resolved); ; directory = filepath.Dir(directory) {
			for _, candidate := range []string{filepath.Join(directory, "pi-mcp-adapter", "index.ts"), filepath.Join(directory, "node_modules", "pi-mcp-adapter", "index.ts")} {
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					module = candidate
					break
				}
			}
			if module != "" || filepath.Dir(directory) == directory {
				break
			}
		}
	}
	if module == "" {
		return "", "", fmt.Errorf("pi-mcp-adapter@2.32.1 required alongside pi-acp; or set PI_MCP_ADAPTER_PATH")
	}
	module, err = filepath.Abs(module)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(module)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("PI_MCP_ADAPTER_PATH must point to index.ts")
	}
	return piPath, module, nil
}

// writeLauncher 只追加 Pi 官方 extension 参数，标准输入输出直接交给上游。
func writeLauncher(directory, executable string) (string, error) {
	extension := filepath.Join(directory, "extension.ts")
	path := filepath.Join(directory, "pi-launcher")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	source := "#!/bin/sh\nexec " + quote(executable) + " --extension " + quote(extension) + " \"$@\"\n"
	if runtime.GOOS == "windows" {
		// Windows 的批处理入口由 pi-acp 按其原生 .cmd 规则启动。
		if strings.ContainsAny(executable+extension, "\r\n\"%") {
			return "", fmt.Errorf("unsupported Windows launcher path")
		}
		path += ".cmd"
		source = "@\"" + executable + "\" --extension \"" + extension + "\" %*\r\n"
	}
	return path, os.WriteFile(path, []byte(source), 0700)
}
