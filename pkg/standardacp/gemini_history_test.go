package standardacp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	agentclient "github.com/JieWaZi/acp-go/pkg/agentclient"
)

const geminiTestID = acp.SessionId("cd124012-e501-49d5-adfc-3870d507db23")

func TestGeminiRestorePreservesExactNativeHistoryAcrossFailedLoad(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	originalName := geminiTestTranscriptName()
	original := geminiTestTranscript(cwd, string(geminiTestID))
	originalPath := filepath.Join(chats, originalName)
	writeGeminiTestFile(t, originalPath, original)

	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(chats)
	if err != nil || len(files) != 2 || files[0].Name() >= originalName {
		t.Fatalf("expected a separate, sorted-first transcript: %v %v", files, err)
	}
	backup := filepath.Join(chats, files[0].Name())
	assertGeminiTestFile(t, originalPath, original)
	assertGeminiTestFile(t, backup, original)

	// A failed native load may append startup-only history to the colliding
	// filename. The preflight must leave the recoverable snapshot intact on retry.
	collided := append(bytes.Clone(original), []byte(`{"$set":{"messages":[],"lastUpdated":"2030-01-01T00:00:00Z"}}`+"\n")...)
	writeGeminiTestFile(t, originalPath, collided)
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err != nil {
		t.Fatal(err)
	}
	assertGeminiTestFile(t, backup, original)
	assertGeminiTestFile(t, originalPath, collided)
}

func TestGeminiRestoreRefusesCollidingFilenameWithDifferentNativeID(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	wrong := geminiTestTranscript(cwd, "cd124012-ffff-4999-aaaa-000000000000")
	path := filepath.Join(chats, geminiTestTranscriptName())
	writeGeminiTestFile(t, path, wrong)
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err == nil {
		t.Fatal("unrelated session could be touched by native startup")
	}
	assertGeminiTestFile(t, path, wrong)
	if files, err := os.ReadDir(chats); err != nil || len(files) != 1 {
		t.Fatalf("unexpected files: %v %v", files, err)
	}
}

func TestGeminiRestoreRefusesWrongProjectOwnership(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	writeGeminiTestFile(t, filepath.Join(filepath.Dir(chats), ".project_root"), []byte(t.TempDir()))
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err == nil {
		t.Fatal("a workspace registry entry cannot override the native ownership marker")
	}
}

func TestGeminiRestoreKeepsUnrelatedNativeFilesUnchanged(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	old := filepath.Join(chats, "session-2026-01-01T00-00-cd124012.jsonl")
	writeGeminiTestFile(t, old, geminiTestTranscript(cwd, string(geminiTestID)))
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err != nil {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(chats); err != nil || len(files) != 1 {
		t.Fatalf("expected untouched non-colliding transcript: %v %v", files, err)
	}
}

func TestGeminiRestoreDoesNotStageBackupForCanceledLoad(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	writeGeminiTestFile(t, filepath.Join(chats, geminiTestTranscriptName()), geminiTestTranscript(cwd, string(geminiTestID)))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := prepareGeminiRestore(ctx, environment, cwd, geminiTestID); err == nil {
		t.Fatal("canceled restore proceeded")
	}
	if files, err := os.ReadDir(chats); err != nil || len(files) != 1 {
		t.Fatalf("unexpected files: %v %v", files, err)
	}
}

func TestGeminiRestoreDoesNotShadowNewerUnprotectedHistory(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	name := geminiTestTranscriptName()
	originalPath := filepath.Join(chats, name)
	original := geminiTestTranscript(cwd, string(geminiTestID))
	writeGeminiTestFile(t, originalPath, original)
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err != nil {
		t.Fatal(err)
	}
	newer := append(bytes.Clone(original), []byte(geminiTestFollowup)...)
	writeGeminiTestFile(t, originalPath, newer)
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err == nil {
		t.Fatal("expected refusal to load while a newer record can collide")
	}
	assertGeminiTestFile(t, filepath.Join(chats, geminiTestBackupName(name)), original)
	assertGeminiTestFile(t, originalPath, newer)
}

func TestGeminiRestoreProtectsMediaOnlyUserHistory(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	name := geminiTestTranscriptName()
	original := bytes.Replace(geminiTestTranscript(cwd, string(geminiTestID)),
		[]byte(`{"text":"preserve this prompt"}`), []byte(`{"inlineData":{"mimeType":"image/png","data":"AA=="}}`), 1)
	writeGeminiTestFile(t, filepath.Join(chats, name), original)
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err != nil {
		t.Fatal(err)
	}
	assertGeminiTestFile(t, filepath.Join(chats, geminiTestBackupName(name)), original)
}

func TestGeminiRestoreRejectsOversizedCollisionWithoutWriting(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	path := filepath.Join(chats, geminiTestTranscriptName())
	writeGeminiTestFile(t, path, geminiTestTranscript(cwd, string(geminiTestID)))
	if err := os.Truncate(path, (64<<20)+1); err != nil {
		t.Fatal(err)
	}
	if err := prepareGeminiRestore(t.Context(), environment, cwd, geminiTestID); err == nil {
		t.Fatal("expected a bounded preflight read")
	}
	if files, err := os.ReadDir(chats); err != nil || len(files) != 1 {
		t.Fatalf("unexpected files: %v %v", files, err)
	}
}

const geminiTestFollowup = `{"id":"followup","type":"user","content":"a new turn"}
{"$set":{"lastUpdated":"2030-01-01T00:00:00Z"}}
`

func TestGeminiRestoreProtectsTranscriptBeforeNativeLoad(t *testing.T) {
	environment, cwd, chats := geminiRestoreFixture(t)
	name := geminiTestTranscriptName()
	original := geminiTestTranscript(cwd, string(geminiTestID))
	writeGeminiTestFile(t, filepath.Join(chats, name), original)
	environment = append(append(os.Environ(), environment...), "SANDBOX=",
		"GEMINI_RESTORE_FIXTURE_CHAT_DIR="+chats, "GEMINI_RESTORE_FIXTURE_FILENAME="+name)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	open := func(environment []string) agentclient.Client {
		t.Helper()
		client, err := NewClient(t.Context(), Options{
			Provider: Gemini, Executable: executable, WorkingDirectory: cwd, Environment: environment,
			PrefixArgs: []string{"-test.run=^TestGeminiCollisionProcess$", "--"},
		}, agentclient.HostCallbacks{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := client.Close(ctx); err != nil {
				t.Error(err)
			}
		})
		if _, err := client.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
			t.Fatal(err)
		}
		// NewClient returns the standard ACP Client, which advertises native load.
		if _, err := client.LoadSession(t.Context(), acp.LoadSessionRequest{SessionId: geminiTestID, Cwd: cwd}); err != nil {
			t.Fatal(err)
		}
		return client
	}
	first := open(environment)
	assertGeminiTestFile(t, filepath.Join(chats, geminiTestBackupName(name)), original)
	prompt, err := first.ReservePrompt(t.Context(), acp.PromptRequest{
		SessionId: geminiTestID, Prompt: []acp.ContentBlock{acp.TextBlock("a new turn")},
	}, agentclient.PromptOptions{HostInvocationID: "followup"})
	if err != nil {
		t.Fatal(err)
	}
	if result := prompt(); result.Receipt.OperationError != nil || result.Outcome == nil ||
		result.Outcome.NativeFailure != nil || result.Outcome.StopReason == nil || *result.Outcome.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("continuation did not complete: %#v", result)
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	open(append(environment, "GEMINI_RESTORE_FIXTURE_EXPECT_FOLLOWUP=1"))
	assertGeminiTestFile(t, filepath.Join(chats, geminiTestBackupName(name)), append(bytes.Clone(original), []byte(geminiTestFollowup)...))
	if files, err := os.ReadDir(chats); err != nil || len(files) != 2 {
		t.Fatalf("retry must reuse the protected transcript: %v %v", files, err)
	}
}

func geminiTestBackupName(original string) string {
	return "session-!acp-go-" + strings.TrimPrefix(original, "session-")
}

func TestGeminiCollisionProcess(t *testing.T) {
	chats := os.Getenv("GEMINI_RESTORE_FIXTURE_CHAT_DIR")
	if chats == "" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		fmt.Println("0.62.0")
		os.Exit(0)
	}
	input, output := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	name := os.Getenv("GEMINI_RESTORE_FIXTURE_FILENAME")
	selectedPath := filepath.Join(chats, geminiTestBackupName(name))
	for {
		var request struct {
			// ID correlates a synthetic response with its ACP request.
			ID json.RawMessage `json:"id"`
			// Method selects the requested native operation.
			Method string `json:"method"`
			// Params carries the session ID and workspace.
			Params acp.LoadSessionRequest `json:"params"`
		}
		if err := input.Decode(&request); err != nil {
			os.Exit(0)
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{
				"protocolVersion": 1, "agentInfo": map[string]string{"name": "gemini-cli", "version": "0.62.0"},
				"agentCapabilities": map[string]any{"loadSession": true},
			}
		case "session/load":
			backup, _ := os.ReadFile(selectedPath)
			original := geminiTestTranscript(request.Params.Cwd, string(request.Params.SessionId))
			collided := append(bytes.Clone(original), []byte(`{"$set":{"messages":[]}}`+"\n")...)
			expected := original
			if os.Getenv("GEMINI_RESTORE_FIXTURE_EXPECT_FOLLOWUP") == "1" {
				expected = append(bytes.Clone(original), []byte(geminiTestFollowup)...)
			}
			if err := os.WriteFile(filepath.Join(chats, name), collided, 0o600); err != nil || !bytes.Equal(backup, expected) {
				response["error"] = map[string]any{"code": -32603, "message": "native history was not protected before load"}
				break
			}
			// The fixed native Load response omits the ID and replays transcript
			// updates correlated with that ID before publishing session metadata.
			_ = output.Encode(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{"sessionId": request.Params.SessionId, "update": map[string]any{
					"sessionUpdate": "user_message_chunk", "content": map[string]string{"type": "text", "text": "preserve this prompt"},
				}},
			})
			response["result"] = map[string]any{}
		case "session/prompt":
			file, err := os.OpenFile(selectedPath, os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				os.Exit(2)
			}
			if _, err := file.WriteString(geminiTestFollowup); err != nil || file.Close() != nil {
				os.Exit(2)
			}
			response["result"] = map[string]any{"stopReason": "end_turn"}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "unsupported fixture method"}
		}
		_ = output.Encode(response)
	}
}

func geminiRestoreFixture(t *testing.T) ([]string, string, string) {
	t.Helper()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	profile := filepath.Join(home, ".gemini")
	chats := filepath.Join(profile, "tmp", "workspace-slug", "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	key := cwd
	if filepath.Separator == '\\' {
		key = strings.ToLower(cwd)
	}
	writeGeminiTestFile(t, filepath.Join(profile, "projects.json"), []byte(fmt.Sprintf(`{"projects":{%q:"workspace-slug"}}`, key)))
	writeGeminiTestFile(t, filepath.Join(filepath.Dir(chats), ".project_root"), []byte(key))
	return []string{"GEMINI_CLI_HOME=" + home, "HOME=" + t.TempDir()}, cwd, chats
}

func geminiTestTranscriptName() string {
	return "session-" + time.Now().UTC().Format("2006-01-02T15-04") + "-cd124012.jsonl"
}

func geminiTestTranscript(cwd, id string) []byte {
	return []byte(fmt.Sprintf(`{"sessionId":%q,"projectHash":"%x","startTime":"2026-11-05T00:00:00Z","lastUpdated":"2026-11-05T00:00:01Z","kind":"main"}
{"id":"prompt","type":"user","content":[{"text":"preserve this prompt"}]}
`, id, sha256.Sum256([]byte(cwd))))
}

func writeGeminiTestFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertGeminiTestFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("unexpected transcript bytes in %s: %q %v", path, got, err)
	}
}
