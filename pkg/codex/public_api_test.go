package codex_test

import (
	"context"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/codex"

	acp "github.com/coder/acp-go-sdk"
)

// TestPublicAPICompiles 验证外部模块可以通过规范 import 使用 Codex 构造入口。
func TestPublicAPICompiles(t *testing.T) {
	var _ acp.Agent = (*codex.Agent)(nil)
	constructor := codex.NewAgent
	var _ func(context.Context, codex.Config) (*codex.Agent, error) = constructor
}
