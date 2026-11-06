package cursor

import (
	_ "embed"
	"encoding/base64"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed acp_usage_preload.cjs
var cursorUsagePreload string

// cursorUsageEnvironment freezes the child environment without creating files or starting native resources.
// A data URL avoids a private-script file dependency and keeps path spaces out of Node arguments.
func cursorUsageEnvironment(executable, directory string, environment []string) []string {
	const prefix = "import{createRequire}from'node:module';const require=createRequire(process.cwd()+'/.acp-go-cursor-usage.cjs');\n"
	preload := "--import=data:text/javascript;base64," + base64.StdEncoding.EncodeToString([]byte(prefix+cursorUsagePreload))
	options := envValue(environment, "NODE_OPTIONS")
	if options != "" {
		options += " "
	}
	options += preload
	result := make([]string, 0, len(environment)+3)
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		if cursorUsageEnvironmentKey(key, "NODE_OPTIONS") ||
			cursorUsageEnvironmentKey(key, "ACP_GO_CURSOR_USAGE_EVENT_DIR") ||
			cursorUsageEnvironmentKey(key, "ACP_GO_CURSOR_USAGE_ARTIFACT_DIR") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"NODE_OPTIONS="+options,
		"ACP_GO_CURSOR_USAGE_EVENT_DIR="+directory,
		"ACP_GO_CURSOR_USAGE_ARTIFACT_DIR="+filepath.Dir(executable),
	)
}

func cursorUsageEnvironmentKey(left, right string) bool {
	return left == right || runtime.GOOS == "windows" && strings.EqualFold(left, right)
}
