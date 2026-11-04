package nativeacp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvironmentValue reads the effective child environment; nil inherits the parent.
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

// WithEnvironment returns a new child environment without mutating the parent.
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

// ResolveCommand searches the child PATH; an explicit path never falls back to another installation.
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

// commandSuffixes includes executable and npm batch entry points on Windows.
func commandSuffixes() []string {
	if runtime.GOOS == "windows" {
		return []string{"", ".exe", ".cmd", ".bat"}
	}
	return []string{""}
}

// CommandContext freezes the resolved executable and complete child environment.
// Runtime, version and authentication callers share the same platform launcher.
func CommandContext(ctx context.Context, executable string, args []string, environment []string) (*exec.Cmd, error) {
	if environment == nil {
		environment = os.Environ()
	}
	environment = append([]string{}, environment...)
	resolved, err := ResolveCommand(executable, environment)
	if err != nil {
		return nil, err
	}
	command, err := platformCommand(ctx, resolved, append([]string(nil), args...), environment)
	if err != nil {
		return nil, err
	}
	command.Env = environment
	prepareProcess(command)
	return command, nil
}
