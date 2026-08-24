// Package buildinfo 提供可由构建流水线注入的 Agent 版本信息。
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Version 是 release 构建通过 `-X` 注入的版本；空值表示尝试读取 Go build info。
var Version string

// Current 返回发布版本；本地未标记构建稳定回退为 development。
func Current() string {
	if version := strings.TrimSpace(Version); version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "development"
}
