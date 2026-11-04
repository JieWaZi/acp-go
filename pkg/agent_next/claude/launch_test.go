package claude_test

import (
	"os"
	"strings"
	"testing"

	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude"
)

func TestLaunchRejectsNativeOverridesInPrefix(t *testing.T) {
	const injected = "prefix-synthetic-secret"
	settings := `{"env":{"ANTHROPIC_API_KEY":"` + injected + `","ANTHROPIC_BASE_URL":"http://127.0.0.1:2222"}}`
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"credential settings", []string{"--settings", settings}},
		{"inline credential settings", []string{"--settings=" + settings}},
		{"managed policy", []string{"--managed-settings={}"}},
		{"load user scope", []string{"--setting-sources=user"}},
		{"remote config", []string{"--client-data-url", "http://127.0.0.1:2222"}},
		{"host auth", []string{"--host-auth-is-org-sign-in"}},
		{"alternate workspace", []string{"--project-config-root=foreign"}},
		{"additional workspace", []string{"--add-dir", "foreign"}},
		{"attached worktree", []string{"-wforeign"}},
		{"debug file outside workspace", []string{"--debug-file", "../protected/claude.log"}},
		{"inline debug file outside workspace", []string{"--debug-file=../protected/claude.log"}},
		{"mcp after wrapper separator", []string{"-test.run=^TestClaudeFixture$", "--", "--mcp-config=foreign.json"}},
		{"plugin discovery", []string{"--plugin-dir", "foreign"}},
		{"plugin without mcp", []string{"--plugin-dir-no-mcp=foreign"}},
		{"permission bypass", []string{"--dangerously-skip-permissions"}},
		{"enable permission bypass", []string{"--allow-dangerously-skip-permissions"}},
		{"tool policy alias", []string{"--allowedTools=Read,Bash"}},
		{"external permission handler", []string{"--permission-prompts", "foreign"}},
		{"external SDK stream", []string{"--sdk-url=ws://127.0.0.1:2222"}},
		{"foreign native resume", []string{"-rforeign"}},
		{"foreign native model", []string{"--model=foreign"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := claude.NewClient(t.Context(), claude.Options{
				Executable: os.Args[0], PrefixArgs: tc.args,
				WorkingDirectory: t.TempDir(), Environment: []string{"ANTHROPIC_API_KEY=selected-synthetic"},
				Launch: claude.LaunchOptions{SettingSources: []string{}},
			}, agentclient.HostCallbacks{})
			if client != nil {
				_ = client.Close(t.Context())
			}
			if err == nil || !strings.Contains(err.Error(), "Claude startup") {
				t.Fatalf("expected native startup override rejection, got %v", err)
			}
			if strings.Contains(err.Error(), injected) {
				t.Fatal("startup rejection disclosed the supplied settings value")
			}
		})
	}
}

func TestLaunchAllowsWrapperAndDiagnosticPrefix(t *testing.T) {
	startMode(t, agentclient.HostCallbacks{}, "", func(options *claude.Options) {
		options.PrefixArgs = append(options.PrefixArgs, "--wrapper-option=fixture", "--debug=hooks,mcp", "-dworktree")
	})
}
