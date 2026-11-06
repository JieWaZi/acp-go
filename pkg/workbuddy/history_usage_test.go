package workbuddy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/workbuddy"
)

const workBuddyHistoryRequest = "0198a1b2c3d47e5f8a9b0c1d2e3f4a5b"

func TestWorkBuddyPromptUsageFromBoundHistory(t *testing.T) {
	for _, scenario := range []string{"subtotal", "reasoning", "reasoning-missing", "reasoning-invalid", "no-receipt", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			// The fixture's short workspace has no suffix hash.
			slug := strings.Trim(regexp.MustCompile(`[-/\\:]+`).ReplaceAllString(cwd, "-"), "-")
			history := filepath.Join(home, "projects", slug, "workbuddy-session.jsonl")
			if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
				t.Fatal(err)
			}
			old, err := json.Marshal(workBuddyHistoryRow(cwd, "workbuddy-session", workBuddyHistoryRequest, "old", "stale", 91))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(history, append(old, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			client, err := workbuddy.NewClient(t.Context(), workbuddy.Options{
				Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeWorkBuddy$", "--"}, WorkingDirectory: cwd,
				Environment: append(os.Environ(), "WORKBUDDY_FIXTURE=history-"+scenario, "CODEBUDDY_CONFIG_DIR="+home,
					"WORKBUDDY_HISTORY="+history, "WORKBUDDY_HISTORY_CWD="+cwd, "WORKBUDDY_TRACE="+filepath.Join(home, "trace")),
			}, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(t.Context()) })
			if _, err := client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			session, err := client.NewSession(t.Context(), acp.NewSessionRequest{Cwd: cwd})
			if err != nil {
				t.Fatal(err)
			}
			run, err := client.ReservePrompt(t.Context(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("fixture")}}, agentclient.PromptOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result := run()
			if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
				t.Fatalf("missing usage: %+v", result)
			}
			usage := result.Outcome.Turn.Usage
			if usage.Complete || usage.InputTokens != nil || usage.CacheReadTokens != nil || usage.CacheWriteTokens != nil ||
				!usage.ContextKnown || len(usage.Observations) != 1 {
				t.Fatalf("unexpected usage coverage: %+v", usage)
			}
			observation := usage.Observations[0]
			if observation.Coverage != "latest_model_request" || observation.Tokens.OutputTokens == nil || *observation.Tokens.OutputTokens != 11 {
				t.Fatalf("native auxiliary sample was lost: %+v", observation)
			}
			if scenario == "no-receipt" || scenario == "conflict" {
				if usage.OutputTokens != nil || len(usage.Models) != 0 || usage.Model != "" {
					t.Fatalf("unverified history was counted: %+v", usage)
				}
				return
			}
			if usage.Coverage != "observed_requests" || usage.OutputTokens == nil || *usage.OutputTokens != 18 || len(usage.Models) != 2 {
				t.Fatalf("unexpected observed output subtotal: %+v", usage)
			}
			if usage.Model != "" {
				t.Fatalf("multiple actual native models were collapsed into one: %+v", usage)
			}
			if scenario == "reasoning" {
				if usage.ReasoningTokens == nil || *usage.ReasoningTokens != 14 {
					t.Fatalf("incorrect measured reasoning subtotal: %+v", usage)
				}
			} else if usage.ReasoningTokens != nil {
				t.Fatalf("unverified reasoning was counted: %+v", usage)
			}
			for model, want := range map[string]int64{"native-a": 7, "native-b": 11} {
				part := usage.Models[model]
				if part.OutputTokens == nil || *part.OutputTokens != want {
					t.Fatalf("incorrect %s subtotal: %+v", model, part)
				}
				if scenario == "reasoning" || strings.HasPrefix(scenario, "reasoning-") && model == "native-b" {
					if part.ReasoningTokens == nil || *part.ReasoningTokens != want-2 {
						t.Fatalf("incorrect %s reasoning subtotal: %+v", model, part)
					}
				} else if part.ReasoningTokens != nil {
					t.Fatalf("unverified %s reasoning was counted: %+v", model, part)
				}
			}
		})
	}
}

func workBuddyHistoryRow(cwd, session, request, id, model string, output int64) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "cwd": cwd, "sessionId": session,
		"providerData": map[string]any{"conversationRequestId": request, "messageId": id, "model": model,
			"rawUsage": map[string]any{"prompt_tokens": 20, "completion_tokens": output, "total_tokens": 20 + output,
				"cache_read_input_tokens": 12, "cache_creation_input_tokens": 3},
			"usage": map[string]any{"outputTokens": output}},
	}
}

func writeWorkBuddyHistoryFixture(scenario string) map[string]any {
	file, err := os.OpenFile(os.Getenv("WORKBUDDY_HISTORY"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(6)
	}
	cwd := os.Getenv("WORKBUDDY_HISTORY_CWD")
	session := "workbuddy-session"
	enc := json.NewEncoder(file)
	duplicate := int64(7)
	if scenario == "history-conflict" {
		duplicate = 8
	}
	functionCall := workBuddyHistoryRow(cwd, session, workBuddyHistoryRequest, "one", "native-a", 7)
	functionCall["type"] = "function_call"
	delete(functionCall, "role")
	for _, row := range []map[string]any{
		functionCall,
		workBuddyHistoryRow(cwd, session, workBuddyHistoryRequest, "one", "native-a", duplicate),
		workBuddyHistoryRow(cwd, session, workBuddyHistoryRequest, "two", "native-b", 11),
		workBuddyHistoryRow(cwd, session, "0198a1b2c3d47e5f8a9b0c1d2e3f4a6b", "unrelated", "native-c", 51),
		workBuddyHistoryRow(cwd, "other-session", workBuddyHistoryRequest, "foreign", "native-c", 81),
		workBuddyHistoryRow(filepath.Join(cwd, "other"), session, workBuddyHistoryRequest, "wrong-workspace", "native-c", 81),
	} {
		if strings.HasPrefix(scenario, "history-reasoning") {
			data := row["providerData"].(map[string]any)
			raw := data["rawUsage"].(map[string]any)
			reasoning := raw["completion_tokens"].(int64) - 2
			if data["messageId"] == "one" && scenario == "history-reasoning-invalid" {
				reasoning = raw["completion_tokens"].(int64) + 1
			}
			if data["messageId"] != "one" || scenario != "history-reasoning-missing" {
				raw["completion_tokens_details"] = map[string]int64{"reasoning_tokens": reasoning}
			}
		}
		if err := enc.Encode(row); err != nil {
			os.Exit(6)
		}
	}
	if file.Close() != nil {
		os.Exit(6)
	}
	result := map[string]any{"stopReason": "end_turn", "userMessageId": "native-user-message"}
	if scenario != "history-no-receipt" {
		result["_meta"] = map[string]any{"codebuddy.ai/conversationRequestId": workBuddyHistoryRequest}
	}
	return result
}
