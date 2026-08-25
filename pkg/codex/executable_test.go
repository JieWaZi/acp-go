package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestPrepareExecutableHonorsExplicitPath 验证显式 CODEX_PATH 失败时绝不静默回退 PATH。
func TestPrepareExecutableHonorsExplicitPath(t *testing.T) {
	t.Parallel()

	lookupCalled := false
	_, err := prepareExecutable(
		context.Background(),
		Config{
			Logger:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
			CodexPath: "/missing/codex",
		},
		func(string) (string, error) {
			lookupCalled = true
			return "/path/codex", nil
		},
		func(context.Context, string, commandOptions) ([]byte, error) {
			return nil, errors.New("not executable")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "/missing/codex") {
		t.Fatalf("显式路径错误为 %v，期望包含原始路径", err)
	}
	if lookupCalled {
		t.Fatal("显式 CODEX_PATH 失败后不应查询 PATH")
	}
}

// TestPrepareExecutableUsesPATHWhenExplicitPathIsEmpty 验证仅在未配置 CODEX_PATH 时查询 PATH。
func TestPrepareExecutableUsesPATHWhenExplicitPathIsEmpty(t *testing.T) {
	t.Parallel()

	got, err := prepareExecutable(
		context.Background(),
		Config{Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))},
		func(name string) (string, error) {
			if name != "codex" {
				t.Fatalf("查询名称为 %q，期望 codex", name)
			}
			return "/path/codex", nil
		},
		func(_ context.Context, path string, options commandOptions) ([]byte, error) {
			if path != "/path/codex" || len(options.Args) != 1 || options.Args[0] != "--version" {
				t.Fatalf("版本命令为 %q %v", path, options.Args)
			}
			return []byte("codex-cli 0.148.0\n"), nil
		},
	)
	if err != nil {
		t.Fatalf("准备可执行文件失败: %v", err)
	}
	if got.Path != "/path/codex" || got.Version != verifiedCodexVersion {
		t.Fatalf("可执行文件为 %#v", got)
	}
}

// TestPrepareExecutableWarnsForUnverifiedVersion 验证非基线版本只写诊断警告并继续。
func TestPrepareExecutableWarnsForUnverifiedVersion(t *testing.T) {
	t.Parallel()

	var diagnostics bytes.Buffer
	got, err := prepareExecutable(
		context.Background(),
		Config{
			Logger:    slog.New(slog.NewTextHandler(&diagnostics, nil)),
			CodexPath: "/opt/codex",
		},
		func(string) (string, error) { return "", errors.New("unexpected lookup") },
		func(context.Context, string, commandOptions) ([]byte, error) {
			return []byte("codex-cli 0.149.1\n"), nil
		},
	)
	if err != nil {
		t.Fatalf("非基线版本不应阻止启动: %v", err)
	}
	if got.Version != "0.149.1" {
		t.Fatalf("解析版本为 %q", got.Version)
	}
	if !strings.Contains(diagnostics.String(), "0.149.1") || !strings.Contains(diagnostics.String(), verifiedCodexVersion) {
		t.Fatalf("版本警告为 %q", diagnostics.String())
	}
}

// TestPrepareExecutablePassesPrefixArgsAndEnvironment 验证探测与长期进程共享同一启动配置。
func TestPrepareExecutablePassesPrefixArgsAndEnvironment(t *testing.T) {
	t.Parallel()

	environment := []string{"PATH=/custom/bin", "CODEX_HOME=/custom/home"}
	_, err := prepareExecutable(
		context.Background(),
		Config{
			Logger:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
			CodexPath:   "/opt/codex",
			PrefixArgs:  []string{"--profile", "team"},
			Environment: environment,
		},
		func(string) (string, error) { return "", errors.New("unexpected lookup") },
		func(_ context.Context, path string, options commandOptions) ([]byte, error) {
			if path != "/opt/codex" {
				t.Fatalf("版本探测路径为 %q", path)
			}
			if got := strings.Join(options.Args, " "); got != "--profile team --version" {
				t.Fatalf("版本探测参数为 %q", got)
			}
			if got := strings.Join(options.Environment, "\n"); got != strings.Join(environment, "\n") {
				t.Fatalf("版本探测环境为 %q", got)
			}
			return []byte("codex-cli 0.148.0\n"), nil
		},
	)
	if err != nil {
		t.Fatalf("准备可执行文件失败: %v", err)
	}
}

// TestEnvironmentLookupFromListUsesCompleteSnapshot 验证显式环境不回退宿主且同名键以后值为准。
func TestEnvironmentLookupFromListUsesCompleteSnapshot(t *testing.T) {
	t.Parallel()

	lookup := environmentLookupFromList([]string{
		"NO_BROWSER=1",
		"CODEX_API_KEY=old",
		"CODEX_API_KEY=current",
	})
	if lookup("NO_BROWSER") != "1" || lookup("CODEX_API_KEY") != "current" {
		t.Fatal("显式环境未按完整快照读取")
	}
	if lookup("PATH") != "" {
		t.Fatal("显式环境不应回退宿主 PATH")
	}
}

// TestParseCodexVersionRejectsUnexpectedOutput 验证无法识别的版本输出在启动前失败。
func TestParseCodexVersionRejectsUnexpectedOutput(t *testing.T) {
	t.Parallel()

	if _, err := parseCodexVersion([]byte("development build")); !errors.Is(err, ErrInvalidCodexVersion) {
		t.Fatalf("版本解析错误为 %v，期望 %v", err, ErrInvalidCodexVersion)
	}
}
