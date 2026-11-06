package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
)

const usageTestNonce = "135e671c-dd8b-4ce3-879b-21788912412e"

func TestCursorUsageJournalKeepsObservedModelCounters(t *testing.T) {
	var first, second cursorUsageEvent
	base := `{"request_id":"` + usageTestRequest + `","user_message_id":"` + usageTestUser + `","run_id":"1394fdfa-0fe3-443a-81d6-1eb21c3e1459","run_index":1,"model":"composer-2.5","input_tokens":12,"output_tokens":3,"cache_read_tokens":2,"cache_write_tokens":0}`
	if err := json.Unmarshal([]byte(base), &first); err != nil {
		t.Fatal(err)
	}
	usage := sumCursorUsageEvents([]cursorUsageEvent{first}, cursorACPUsageIdentity{requestID: usageTestRequest, userID: usageTestUser})
	if usage == nil || usage.Model != "composer-2.5" {
		t.Fatalf("native model identity lost: %+v", usage)
	}
	if err := json.Unmarshal([]byte(base), &second); err != nil {
		t.Fatal(err)
	}
	second.Run, second.Index = "2394fdfa-0fe3-443a-81d6-1eb21c3e1459", 2
	usage = sumCursorUsageEvents([]cursorUsageEvent{first, second}, cursorACPUsageIdentity{requestID: usageTestRequest, userID: usageTestUser})
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 20 || usage.Model != "composer-2.5" {
		t.Fatalf("same-model runs lost attribution or doubled counters: %+v", usage)
	}
}

func TestCursorUsageJournalRequiresFreshNativeProof(t *testing.T) {
	for _, scenario := range []string{"known", "launcher-version", "native-version-mismatch", "zero", "unknown-cache", "duplicate", "later-run-index", "conflict", "foreign-nonce", "foreign-session", "foreign-request", "foreign-user", "foreign-version", "foreign-source", "foreign-schema", "invalid-user", "invalid-cache", "missing", "stale", "bad-health", "no-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			db, root := usageTestDB(t)
			usageTestTurn(t, db, usageTestOldRequest, usageTestOldUser)
			directory := t.TempDir()
			health := map[string]any{"schema": 1, "status": "ready", "version": "2026.11.05-86047df"}
			if scenario == "bad-health" {
				health["status"] = "rejected"
			}
			usageTestJSON(t, filepath.Join(directory, "health.json"), health)
			scope := acpclient.PromptJournalScope{Version: "2026.11.05-86047df", SessionID: acp.SessionId(usageTestSession)}
			if scenario == "launcher-version" {
				scope.Version, scope.LauncherVersion = "", scope.Version
			}
			if scenario == "native-version-mismatch" {
				scope.Version, scope.LauncherVersion = "2026.10.22-27dcf5f", scope.Version
			}
			capture := captureCursorUsageJournal(t.Context(), scope, root, directory, usageTestNonce)
			if scenario == "bad-health" || scenario == "native-version-mismatch" {
				if capture != nil {
					t.Fatal("rejected preload enabled collection")
				}
				return
			}
			if capture == nil {
				t.Fatal("capture unavailable")
			}
			if scenario != "stale" {
				usageTestTurn(t, db, usageTestRequest, usageTestUser)
			}
			event := map[string]any{"schema": 1, "source": "cursor.acp.turn_ended", "version": "2026.11.05-86047df", "nonce": usageTestNonce, "session_id": usageTestSession,
				"request_id": usageTestRequest, "user_message_id": usageTestUser, "run_id": "1394fdfa-0fe3-443a-81d6-1eb21c3e1459", "run_index": 1,
				"input_tokens": 12941, "output_tokens": 383, "cache_read_tokens": 7456, "cache_write_tokens": 0}
			switch scenario {
			case "zero":
				event["input_tokens"], event["output_tokens"], event["cache_read_tokens"] = 0, 0, 0
			case "unknown-cache":
				delete(event, "cache_write_tokens")
			case "foreign-nonce":
				event["nonce"] = "235e671c-dd8b-4ce3-879b-21788912412e"
			case "foreign-session":
				event["session_id"] = "43b71216-7e10-47c9-b423-997ec7209ff2"
			case "foreign-request":
				event["request_id"] = usageTestOldRequest
			case "foreign-user":
				event["user_message_id"] = usageTestOldUser
			case "foreign-version":
				event["version"] = "unverified"
			case "foreign-source":
				event["source"] = "foreign"
			case "foreign-schema":
				event["schema"] = 2
			case "invalid-user":
				event["user_message_id"] = "not-a-uuid"
			case "invalid-cache":
				event["cache_read_tokens"] = 12942
			case "later-run-index":
				event["run_index"] = 1000
			}
			if scenario != "missing" {
				usageTestJSON(t, filepath.Join(directory, usageTestNonce+"-first.json"), event)
			}
			if scenario == "duplicate" || scenario == "conflict" {
				if scenario == "conflict" {
					event["output_tokens"] = 384
				}
				usageTestJSON(t, filepath.Join(directory, usageTestNonce+"-duplicate.json"), event)
			}
			receipt := acpclient.PromptJournalReceipt{StopReason: acp.Ptr(acp.StopReasonEndTurn)}
			if scenario == "no-receipt" {
				receipt.StopReason = nil
			}
			evidence := capture(t.Context(), receipt)
			if evidence.NativeTurnID != "" && evidence.NativeTurnID != usageTestRequest || evidence.Failure != nil || evidence.UnverifiedTerminal {
				t.Fatalf("collector changed native outcome: %+v", evidence)
			}
			want := scenario == "known" || scenario == "launcher-version" || scenario == "zero" || scenario == "unknown-cache" || scenario == "duplicate" || scenario == "later-run-index"
			if !want {
				if evidence.Usage != nil || evidence.NativeTurnID != "" {
					t.Fatalf("unverified usage accepted: %+v", evidence)
				}
				return
			}
			u := evidence.Usage
			if u == nil || evidence.NativeTurnID != usageTestRequest || u.Complete || u.Coverage != "observed_requests" || u.Model != "" || len(u.Models) != 0 {
				t.Fatalf("incorrect scoped usage: %+v", evidence)
			}
			if scenario == "unknown-cache" {
				if u.InputTokens != nil || u.CacheWriteTokens != nil || u.OutputTokens == nil || *u.OutputTokens != 383 {
					t.Fatalf("unknown cache became zero: %+v", u)
				}
				return
			}
			input, output, read := int64(5485), int64(383), int64(7456)
			if scenario == "zero" {
				input, output, read = 0, 0, 0
			}
			if u.InputTokens == nil || *u.InputTokens != input || u.OutputTokens == nil || *u.OutputTokens != output || u.CacheReadTokens == nil || *u.CacheReadTokens != read || u.CacheWriteTokens == nil || *u.CacheWriteTokens != 0 {
				t.Fatalf("wrong counters: %+v", u)
			}
		})
	}
}

func usageTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCursorUsageJournalChecksDisjointRunSum(t *testing.T) {
	first := cursorUsageEvent{Request: usageTestRequest, User: usageTestUser, Run: "1394fdfa-0fe3-443a-81d6-1eb21c3e1459", Index: 1,
		Input: acp.Ptr(int64(12941)), Output: acp.Ptr(int64(383)), CacheRead: acp.Ptr(int64(7456)), CacheWrite: acp.Ptr(int64(0))}
	second := first
	second.Run, second.Index = "2394fdfa-0fe3-443a-81d6-1eb21c3e1459", 2
	identity := cursorACPUsageIdentity{requestID: usageTestRequest, userID: usageTestUser}
	usage := sumCursorUsageEvents([]cursorUsageEvent{first, second}, identity)
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 10970 || usage.OutputTokens == nil || *usage.OutputTokens != 766 || usage.Complete {
		t.Fatalf("independent run subtotal incorrect: %+v", usage)
	}
	first.Output, second.Output = acp.Ptr(int64(cursorUsageCounterLimit)), acp.Ptr(int64(1))
	if sumCursorUsageEvents([]cursorUsageEvent{first, second}, identity) != nil {
		t.Fatal("sum exceeding exact native counter range was accepted")
	}
}
