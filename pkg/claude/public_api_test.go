package claude_test

import (
	"context"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/claude"

	acp "github.com/coder/acp-go-sdk"
)

// TestPublicAPICompiles 验证外部模块可以通过规范 import 使用 Claude 构造入口。
func TestPublicAPICompiles(t *testing.T) {
	var _ acp.Agent = (*claude.Agent)(nil)
	constructor := claude.NewAgent
	var _ func(context.Context, claude.Config) (*claude.Agent, error) = constructor
}
