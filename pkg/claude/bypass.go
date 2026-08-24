package claude

import "os"

// bypassPermissionsAllowed 复用 upstream 的 root/sandbox 安全门槛。
func bypassPermissionsAllowed() bool {
	return !processRunsAsRoot() || os.Getenv("IS_SANDBOX") != ""
}
