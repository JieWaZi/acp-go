package qoder_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
	"github.com/JieWaZi/acp-go/pkg/qoder"
)

const journalSession = "11111111-1111-4111-8111-111111111111"

// These integration cases catch missing journal wiring, cross-turn attribution,
// duplicate accounting, and treating native default zeroes as measured evidence.
func TestQoderPromptUsageFromNativeJournal(t *testing.T) {
	for _, scenario := range []string{"measured", "tool-chain", "foreign", "duplicate", "repeat-response", "fragments", "zero-then-measured", "zero", "unknown-cache", "custom", "conflict", "unsupported", "missing", "no-receipt", "foreign-cwd", "foreign-session", "sidechain", "broken-parent", "invalid-count", "replaced-file", "symlink", "partial-line", "cancelled", "failed"} {
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
			// This controlled short path uses the native non-hashed slug form.
			slug := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-")
			history := filepath.Join(home, "projects", slug, journalSession+".jsonl")
			if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
				t.Fatal(err)
			}
			appendJournalRows(history, journalRow(cwd, journalSession, "old", "", "user", nil), journalRow(cwd, journalSession, "old-answer", "old", "assistant", map[string]any{"input_tokens": 999, "output_tokens": 99, "cache_read_input_tokens": 999}))
			client, err := qoder.NewClient(t.Context(), qoder.Options{Executable: executable, PrefixArgs: []string{"-test.run=^TestNativeQoderJournal$", "--"}, WorkingDirectory: cwd, Environment: append(os.Environ(), "QODER_CONFIG_DIR="+home, "QODER_JOURNAL_FIXTURE="+scenario, "QODER_JOURNAL_PATH="+history, "QODER_JOURNAL_CWD="+cwd)}, agentclient.HostCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close(t.Context()) })
			if _, err = client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
				t.Fatal(err)
			}
			session, err := client.NewSession(t.Context(), acp.NewSessionRequest{Cwd: cwd})
			if err != nil {
				t.Fatal(err)
			}
			for turn := 1; turn <= 2; turn++ {
				run, err := client.ReservePrompt(t.Context(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("fixture")}}, agentclient.PromptOptions{})
				if err != nil {
					t.Fatal(err)
				}
				result := run()
				if result.Outcome == nil || result.Outcome.Turn.Usage == nil {
					t.Fatalf("usage missing: %+v", result)
				}
				usage := result.Outcome.Turn.Usage
				if usage.Complete || usage.InputTokens != nil || usage.ReasoningTokens != nil {
					t.Fatalf("invented complete components: %+v", usage)
				}
				enriched := scenario == "measured" || scenario == "tool-chain" || scenario == "foreign" || scenario == "duplicate" || scenario == "fragments" || scenario == "repeat-response" || scenario == "zero-then-measured" || scenario == "unknown-cache" || scenario == "custom"
				if !enriched {
					if usage.Source == "qoder.history.usage" || usage.CacheReadTokens != nil || usage.CacheWriteTokens != nil {
						t.Fatalf("unproven journal attributed: %+v", usage)
					}
					continue
				}
				if usage.Source != "qoder.history.usage" {
					t.Fatalf("native journal was not consumed: %+v", usage)
				}
				wantInput, wantOutput, wantRead := int64(24), int64(7), int64(10)
				if turn == 2 {
					wantInput, wantOutput, wantRead = 35, 11, 20
				}
				if scenario == "tool-chain" {
					wantInput += 5
					wantOutput += 2
					wantRead += 2
				}
				if usage.OutputTokens == nil || *usage.OutputTokens != wantOutput || usage.Model != "actual-model" {
					t.Fatalf("incorrect turn/model subtotal: %+v", usage)
				}
				model := usage.Models["actual-model"]
				if model.OutputTokens == nil || *model.OutputTokens != wantOutput {
					t.Fatalf("missing actual-model counters: %+v", usage.Models)
				}
				if scenario == "unknown-cache" {
					if usage.CacheReadTokens != nil || usage.CacheWriteTokens != nil {
						t.Fatalf("unknown cache was zero-filled: %+v", usage)
					}
				} else if usage.CacheReadTokens == nil || *usage.CacheReadTokens != wantRead || model.CacheReadTokens == nil || *model.CacheReadTokens != wantRead {
					t.Fatalf("missing measured cache: %+v", usage)
				}
				if turn == 1 && scenario != "unknown-cache" {
					if usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 3 {
						t.Fatalf("cache write missing: %+v", usage)
					}
				} else if usage.CacheWriteTokens != nil {
					t.Fatalf("default zero cache write became known: %+v", usage)
				}
				found := false
				for _, obs := range usage.Observations {
					if obs.Source == "qoder.history.usage" && obs.InputTotalTokens != nil && *obs.InputTotalTokens == wantInput {
						found = true
						if obs.Complete {
							t.Fatal("raw observation marked complete")
						}
					}
				}
				if !found {
					t.Fatalf("raw input was lost: %+v", usage.Observations)
				}
			}
		})
	}
}

func journalRow(cwd, session, id, parent, kind string, usage map[string]any) map[string]any {
	row := map[string]any{"type": kind, "uuid": id, "parentUuid": parent, "sessionId": session, "cwd": cwd, "version": "1.1.83", "isSidechain": false}
	if kind == "assistant" {
		row["message"] = map[string]any{"id": id + "-message", "model": "actual-model", "stop_reason": "end_turn", "usage": usage}
		row["requestTokenAnchor"] = map[string]any{"requestId": id + "-request"}
	}
	return row
}

func appendJournalRows(path string, rows ...map[string]any) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	for _, row := range rows {
		if err := json.NewEncoder(file).Encode(row); err != nil {
			panic(err)
		}
	}
}

func TestNativeQoderJournal(t *testing.T) {
	scenario := os.Getenv("QODER_JOURNAL_FIXTURE")
	if scenario == "" {
		return
	}
	version := "1.1.83"
	if scenario == "unsupported" {
		version = "1.1.84"
	}
	if os.Args[len(os.Args)-1] == "--version" {
		fmt.Println(version)
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	turn := 0
	for scanner.Scan() {
		var frame struct {
			// ID identifies the host RPC call.
			ID json.RawMessage `json:"id"`
			// Method selects the native ACP operation.
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			os.Exit(3)
		}
		reply := func(result any) {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result})
		}
		switch frame.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "qoder-cli", "version": version}})
		case "session/new":
			reply(map[string]any{"sessionId": journalSession})
		case "session/prompt":
			turn++
			input, output, read, write := 24, 7, 10, 3
			if turn == 2 {
				input, output, read, write = 35, 11, 20, 0
			}
			id := fmt.Sprintf("22222222-2222-4222-8222-%012d", turn)
			cwd, path := os.Getenv("QODER_JOURNAL_CWD"), os.Getenv("QODER_JOURNAL_PATH")
			user := journalRow(cwd, journalSession, id, "old-answer", "user", nil)
			usage := map[string]any{"input_tokens": input, "output_tokens": output, "cache_read_input_tokens": read, "cache_creation_input_tokens": write, "request_id": id + "-answer-request"}
			answer := journalRow(cwd, journalSession, id+"-answer", id, "assistant", usage)
			if scenario == "zero" {
				for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
					usage[key] = 0
				}
			}
			if scenario == "unknown-cache" {
				delete(usage, "cache_read_input_tokens")
				usage["cache_creation_input_tokens"] = 0
			}
			if scenario == "foreign-cwd" {
				answer["cwd"] = cwd + "-foreign"
			}
			if scenario == "foreign-session" {
				answer["sessionId"] = "foreign"
			}
			if scenario == "sidechain" {
				answer["isSidechain"] = true
			}
			if scenario == "broken-parent" {
				answer["parentUuid"] = "missing-parent"
			}
			if scenario == "invalid-count" {
				usage["cache_read_input_tokens"] = -1
			}
			if scenario == "custom" {
				answer["modelSource"] = "custom"
				answer["customProviderId"] = "custom-provider"
			}
			if scenario != "missing" {
				appendJournalRows(path, user)
				if scenario == "foreign" {
					appendJournalRows(path, journalRow(cwd, "foreign", id+"-alien", id, "assistant", usage), journalRow(cwd, journalSession, "another-user", id, "user", nil), journalRow(cwd, journalSession, "another-answer", "another-user", "assistant", usage))
				}
				if scenario == "fragments" {
					fragment := journalRow(cwd, journalSession, id+"-fragment", id, "assistant", nil)
					fragment["message"].(map[string]any)["id"] = id + "-answer-message"
					answer["parentUuid"] = id + "-fragment"
					appendJournalRows(path, fragment)
				}
				if scenario == "zero-then-measured" {
					zero := journalRow(cwd, journalSession, id+"-zero", id, "assistant", map[string]any{"input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0})
					zero["message"].(map[string]any)["id"] = id + "-answer-message"
					appendJournalRows(path, zero)
				}
				appendJournalRows(path, answer)
				if scenario == "tool-chain" {
					tool := journalRow(cwd, journalSession, id+"-tool-result", id+"-answer", "user", nil)
					tool["sourceToolAssistantUUID"] = id + "-answer"
					final := journalRow(cwd, journalSession, id+"-tool-final", id+"-tool-result", "assistant", map[string]any{"input_tokens": 5, "output_tokens": 2, "cache_read_input_tokens": 2, "cache_creation_input_tokens": 0, "request_id": id + "-tool-final-request"})
					appendJournalRows(path, tool, final)
					input += 5
					output += 2
				}
				if scenario == "duplicate" {
					appendJournalRows(path, answer)
				}
				if scenario == "repeat-response" {
					answer["uuid"] = id + "-repeat"
					appendJournalRows(path, answer)
				}
				if scenario == "replaced-file" {
					_ = os.Rename(path, path+".old")
					appendJournalRows(path, user, answer)
				}
				if scenario == "symlink" {
					_ = os.Rename(path, path+".old")
					_ = os.Symlink(path+".old", path)
				}
				if scenario == "partial-line" {
					f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					_, _ = f.WriteString("{")
					_ = f.Close()
				}
				if scenario == "conflict" {
					clone := journalRow(cwd, journalSession, id+"-conflict", id, "assistant", map[string]any{"input_tokens": input, "output_tokens": output + 1, "cache_read_input_tokens": read, "request_id": id + "-answer-request"})
					clone["message"].(map[string]any)["id"] = id + "-answer-message"
					appendJournalRows(path, clone)
				}
			}
			stop := "end_turn"
			if scenario == "cancelled" {
				stop = "cancelled"
			}
			if scenario == "failed" {
				stop = "refusal"
			}
			result := map[string]any{"stopReason": stop, "usage": map[string]int{"inputTokens": input, "outputTokens": output, "totalTokens": input + output}, "_meta": map[string]any{"quota": map[string]any{"token_count": map[string]int{"input_tokens": input, "output_tokens": output}}}}
			if scenario != "no-receipt" {
				result["userMessageId"] = id
			}
			reply(result)
		default:
			reply(map[string]any{})
		}
	}
	os.Exit(0)
}
