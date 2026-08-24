package acpserver_test

import (
	"context"
	"io"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/acpserver"
	acp "github.com/coder/acp-go-sdk"
)

// TestPublicAPICompiles 验证外部模块可以组合 Registry 与 Server 公开入口。
func TestPublicAPICompiles(t *testing.T) {
	var _ func(string) (*acpserver.Registry, error) = acpserver.NewRegistry
	var _ func(acp.Agent, io.Reader, io.Writer) (*acpserver.Server, error) = acpserver.New
	var _ func(*acpserver.Server, context.Context) error = (*acpserver.Server).Serve
}
