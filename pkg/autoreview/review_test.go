package autoreview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestReviewRejectsInvalidAuthority 验证非法输出、不完整证据和取消都不能产生自动授权。
func TestReviewRejectsInvalidAuthority(t *testing.T) {
	request := Request{WorkingDirectory: t.TempDir(), Prompt: []acp.ContentBlock{acp.TextBlock("write requested file")}, Tool: acp.ToolCallUpdate{RawInput: map[string]any{"path": "example.txt"}}}
	for _, output := range []string{`null`, `{}`, `{"outcome":"allow_always"}`, "```json\n{\"outcome\":\"allow\"}\n```", `{"outcome":"allow"} trailing`, strings.Repeat("x", 16385)} {
		if _, err := Review(context.Background(), func(context.Context, string) ([]byte, error) { return []byte(output), nil }, request); err == nil {
			t.Fatalf("invalid output authorized: %q", output[:min(len(output), 80)])
		}
	}
	called := false
	runner := func(context.Context, string) ([]byte, error) {
		called = true
		return []byte(`{"outcome":"allow"}`), nil
	}
	incomplete := request
	incomplete.Tool.RawInput = nil
	if _, err := Review(context.Background(), runner, incomplete); err == nil || called {
		t.Fatal("incomplete evidence reached reviewer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := Review(ctx, func(context.Context, string) ([]byte, error) { cancel(); return []byte(`{"outcome":"allow"}`), nil }, request); err != context.Canceled {
		t.Fatalf("cancelled review returned %v", err)
	}
}

// TestMetadataDoesNotExposeModelText 验证审计只保留枚举事实，不把模型原文或未知风险值写出。
func TestMetadataDoesNotExposeModelText(t *testing.T) {
	decision := Decision{Outcome: "allow", RiskLevel: "secret-risk", Rationale: "secret-rationale", UserAuthorization: "secret-auth"}
	data, err := json.Marshal(Metadata(decision, false))
	if err != nil || strings.Contains(string(data), "secret") || !strings.Contains(string(data), `"outcome":"allow"`) {
		t.Fatalf("unsafe metadata: %s, %v", data, err)
	}
	if Metadata(decision, true)["outcome"] != "ask" || Metadata(Decision{Outcome: "deny"}, false)["outcome"] != "ask" {
		t.Fatal("failed or denied review did not defer to human")
	}
}
