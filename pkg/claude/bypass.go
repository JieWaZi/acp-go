package claude

// bypassPermissionsAllowed 复用 root/sandbox 安全门槛。
func bypassPermissionsAllowed(environment []string) bool {
	return !processRunsAsRoot() || claudeEnvironmentValue(environment, "IS_SANDBOX") != ""
}
