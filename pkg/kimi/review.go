package kimi

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/autoreview"
	"github.com/JieWaZi/acp-go/pkg/nativeacp"
	"github.com/pelletier/go-toml/v2"
)

// permissionReviewer 使用同一 Kimi 登录与模型运行无工具审查，失败由原生门禁转人工。
func permissionReviewer(config Config, directory string, environment []string) (func(context.Context, autoreview.Request) (autoreview.Decision, error), error) {
	work := filepath.Join(directory, "permission-review")
	if err := os.Mkdir(work, 0700); err != nil {
		return nil, err
	}
	share := filepath.Join(work, "share")
	if err := os.Mkdir(share, 0700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "config.toml"))
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	if err := toml.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	// 审查进程不加载用户 Hook、MCP 或其他可执行扩展。
	delete(values, "hooks")
	delete(values, "mcp")
	values["default_yolo"] = false
	data, err = toml.Marshal(values)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(share, "config.toml"), data, 0600); err != nil {
		return nil, err
	}
	if err := os.Symlink(filepath.Join(directory, "credentials"), filepath.Join(share, "credentials")); err != nil {
		return nil, err
	}
	environment = nativeacp.WithEnvironment(environment, "KIMI_SHARE_DIR", share)
	promptPath := filepath.Join(work, "system.md")
	if err := os.WriteFile(promptPath, []byte(autoreview.SystemPrompt), 0600); err != nil {
		return nil, err
	}
	spec := filepath.Join(work, "agent.yaml")
	if err := os.WriteFile(spec, []byte("version: 1\nagent:\n  name: Permission reviewer\n  system_prompt_path: ./system.md\n  tools: []\n"), 0600); err != nil {
		return nil, err
	}
	return func(ctx context.Context, request autoreview.Request) (autoreview.Decision, error) {
		return autoreview.Review(ctx, func(ctx context.Context, input string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			args := append([]string{}, config.PrefixArgs...)
			args = append(args, "--agent-file", spec, "--print", "--output-format", "text", "--final-message-only")
			if request.Model != "" {
				args = append(args, "--model", request.Model)
			}
			command := exec.CommandContext(ctx, config.KimiPath, args...)
			command.Env = environment
			command.Dir = work
			command.Stdin = strings.NewReader(input)
			command.Stderr = io.Discard
			command.WaitDelay = time.Second
			output := &limitedReviewOutput{}
			command.Stdout = output
			if err := command.Run(); err != nil {
				return nil, err
			}
			return output.Bytes(), nil
		}, request)
	}, nil
}

// limitedReviewOutput 防止上游异常输出无限占用内存。
type limitedReviewOutput struct {
	// Buffer 保存有界的审查响应。
	bytes.Buffer
}

// Write 超过审查响应上限时返回错误，使调用回退人工。
func (b *limitedReviewOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 16384 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(data)
}
