//go:build !darwin && !linux

package claude

// processRunsAsRoot 在非正式支持平台保持可编译并默认不判定为 root。
func processRunsAsRoot() bool {
	return false
}
