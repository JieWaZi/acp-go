//go:build darwin || linux

package claude

import "os"

// processRunsAsRoot 判断受正式支持的 Unix 平台是否以 root 身份运行。
func processRunsAsRoot() bool {
	return os.Geteuid() == 0
}
