package nativeacp

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvironmentValue 按子进程实际环境读取变量，nil 表示继承宿主。
func EnvironmentValue(environment []string, name string) string {
	if environment == nil {
		return os.Getenv(name)
	}
	for i := len(environment) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(environment[i], "=")
		if ok && (key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			return value
		}
	}
	return ""
}

// WithEnvironment 返回覆盖指定变量的独立环境副本，不修改宿主进程。
func WithEnvironment(environment []string, name, value string) []string {
	if environment == nil {
		environment = os.Environ()
	}
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key != name && !(runtime.GOOS == "windows" && strings.EqualFold(key, name)) {
			result = append(result, entry)
		}
	}
	return append(result, name+"="+value)
}

// ResolveCommand 在子进程 PATH 内解析程序，显式路径失败不会回退到其他安装。
func ResolveCommand(command string, environment []string) (string, error) {
	if strings.ContainsAny(command, `/\`) {
		path, err := filepath.Abs(command)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() || runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
			return "", &exec.Error{Name: command, Err: exec.ErrNotFound}
		}
		return path, nil
	}
	for _, dir := range filepath.SplitList(EnvironmentValue(environment, "PATH")) {
		if dir == "" {
			continue
		}
		for _, suffix := range commandSuffixes() {
			path := filepath.Join(dir, command+suffix)
			info, err := os.Stat(path)
			if err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0) {
				return filepath.Abs(path)
			}
		}
	}
	return "", &exec.Error{Name: command, Err: exec.ErrNotFound}
}

// commandSuffixes 覆盖 npm 在 Windows 上创建的命令入口。
func commandSuffixes() []string {
	if runtime.GOOS == "windows" {
		return []string{"", ".exe", ".cmd", ".bat"}
	}
	return []string{""}
}
