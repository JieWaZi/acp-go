package pi

import (
	"context"
	acp "github.com/coder/acp-go-sdk"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPiForkCloneAndColdResume 通过独立 RPC 子进程验证 clone、上下文、续发和冷恢复。
func TestPiForkCloneAndColdResume(t *testing.T) {
	a, parent := testAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.Prompt(ctx, acp.PromptRequest{SessionId: parent, Prompt: []acp.ContentBlock{acp.TextBlock("before-fork")}}); err != nil {
		t.Fatal(err)
	}
	source, err := a.get(parent)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source.file)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	request := acp.UnstableForkSessionRequest{SessionId: parent, Cwd: target, AdditionalDirectories: []string{source.cwd}, Meta: map[string]any{"sourceCwd": source.cwd, "forkReceiptDirectory": t.TempDir()}}
	child, err := a.UnstableForkSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if child.SessionId == parent {
		t.Fatal("identity reused")
	}
	copied, err := a.findSession(child.SessionId, target)
	if err != nil {
		t.Fatal(err)
	}
	inherited, _ := os.ReadFile(copied)
	if !strings.Contains(string(inherited), "before-fork") {
		t.Fatal("native context lost")
	}
	if _, err := a.Prompt(ctx, acp.PromptRequest{SessionId: child.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("child-only")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CloseSession(ctx, acp.CloseSessionRequest{SessionId: child.SessionId}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: child.SessionId, Cwd: target, AdditionalDirectories: []string{source.cwd}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(copied)
	if !strings.Contains(string(data), "child-only") {
		t.Fatal("child continuation lost on cold resume")
	}
	unchanged, _ := os.ReadFile(source.file)
	if string(unchanged) != string(original) {
		t.Fatal("parent modified")
	}
	request.Meta["forkPosition"] = "earlier"
	if _, err := a.UnstableForkSession(ctx, request); err == nil {
		t.Fatal("historical position accepted")
	}
}

// TestPiSessionAdditionalDirectories 验证已有目录可用、失效或相对目录在启动前明确拒绝。
func TestPiSessionAdditionalDirectories(t *testing.T) {
	a, _ := testAgent(t)
	cwd, project := t.TempDir(), t.TempDir()
	for _, invalid := range []string{"relative", project + "/missing"} {
		if _, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{invalid}}); err == nil {
			t.Fatal("invalid directory accepted")
		}
	}
	if _, err := a.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd, AdditionalDirectories: []string{project}}); err != nil {
		t.Fatal(err)
	}
}
