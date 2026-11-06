package cursor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

func TestCursorUsageClientFixture(t *testing.T) {
	if os.Getenv("CURSOR_USAGE_CLIENT_FIXTURE") != "1" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		fmt.Println(observedVersion)
		os.Exit(0)
	}
	scanner, encoder := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			// ID correlates this JSON-RPC call with its response.
			ID json.RawMessage `json:"id"`
			// Method selects the native operation handled by this fixture.
			Method string `json:"method"`
			// Params lets the fixture inspect private usage association metadata.
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		reply := func(result any) {
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		switch request.Method {
		case "initialize":
			// The official ACP handshake omits agentInfo; a separate CLI probe supplies its version.
			reply(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true}}})
		case "session/new":
			reply(map[string]any{"sessionId": "usage-session", "configOptions": []map[string]any{{"id": "model", "name": "Model", "type": "select", "category": "model", "currentValue": "composer", "options": []map[string]any{{"value": "composer", "name": "Composer"}}}}})
		case "session/set_config_option":
			reply(map[string]any{"configOptions": []map[string]any{{"id": "model", "name": "Model", "type": "select", "category": "model", "currentValue": "composer", "options": []map[string]any{{"value": "composer", "name": "Composer"}}}}})
		case "cursor/list_available_models":
			reply(map[string]any{"models": []any{}})
		case "session/prompt":
			meta, _ := request.Params["_meta"].(map[string]any)
			nonce, _ := meta[cursorUsageNonceKey].(string)
			file, err := os.OpenFile(os.Getenv("CURSOR_USAGE_CLIENT_TRACE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fmt.Fprintln(file, nonce)
			if closeErr := file.Close(); err != nil || closeErr != nil {
				t.Fatal(errors.Join(err, closeErr))
			}
			for _, text := range []string{"one", "two", "three"} {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "usage-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}}})
			}
			reply(map[string]any{"stopReason": "end_turn"})
		case "session/cancel":
		default:
			if len(request.ID) > 0 {
				reply(map[string]any{})
			}
		}
	}
}

func newCursorUsageFixtureClient(t *testing.T, host agentclient.HostCallbacks) (*Client, acp.SessionId, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "nonces")
	client, err := NewClient(context.Background(), Options{Executable: executable, PrefixArgs: []string{"-test.run=^TestCursorUsageClientFixture$", "--"}, Environment: append(os.Environ(), "CURSOR_USAGE_CLIENT_FIXTURE=1", "CURSOR_USAGE_CLIENT_TRACE="+trace, "CURSOR_CONFIG_DIR="+t.TempDir()), UsageHook: true}, host)
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
	initialized, err := client.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if initialized.AgentInfo == nil || initialized.AgentInfo.Name != "cursor" || acpmeta.RuntimeVersion(initialized.AgentInfo.Meta) != observedVersion {
		t.Fatalf("missing native agentInfo lost the launcher version: %+v", initialized.AgentInfo)
	}
	response, err := client.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return client, response.SessionId, trace
}

func TestCursorUsageNonceIsRFC4122Version4(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 128; i++ {
		nonce := newCursorUsageNonce()
		if !pattern.MatchString(nonce) || seen[nonce] {
			t.Fatalf("invalid or reused usage nonce %q", nonce)
		}
		seen[nonce] = true
	}
}

func TestCursorUsageConfigRootUsesSelectedXDGAccountScope(t *testing.T) {
	root := t.TempDir()
	client := &Client{opts: Options{Environment: []string{"HOME=/unused-account", "XDG_CONFIG_HOME=" + root}, WorkingDirectory: t.TempDir()}}
	if actual := client.usageConfigRoot(); actual != filepath.Join(root, "cursor") {
		t.Fatalf("selected XDG account root=%q", actual)
	}
}

func TestCursorUsageReservationPreservesSharedAdmissionAndNonce(t *testing.T) {
	client, id, trace := newCursorUsageFixtureClient(t, agentclient.HostCallbacks{})
	input := acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}, Meta: map[string]any{"caller": "kept"}}
	run, err := client.ReservePrompt(context.Background(), input, agentclient.PromptOptions{BeforeSubmit: func(context.Context, agentclient.PromptSubmission) error { return errors.New("admission denied") }})
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	nonce := client.usage.nonces[id]
	client.mu.Unlock()
	if nonce == "" {
		t.Fatal("reserved input has no usage nonce")
	}
	if _, err = client.ReservePrompt(context.Background(), input, agentclient.PromptOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("shared reservation=%v", err)
	}
	client.mu.Lock()
	after := client.usage.nonces[id]
	client.mu.Unlock()
	if after != nonce {
		t.Fatal("rejected input replaced active nonce")
	}
	result := run()
	if result.Receipt.Submission != agentclient.SubmissionNotSent || result.Receipt.OperationError == nil {
		t.Fatalf("admission=%+v", result)
	}
	if _, err = os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed admission submitted prompt")
	}
	client.mu.Lock()
	remaining := client.usage.nonces[id]
	client.mu.Unlock()
	if remaining != "" {
		t.Fatal("failed admission retained nonce")
	}
	if _, exists := input.Meta[cursorUsageNonceKey]; exists {
		t.Fatal("caller metadata was mutated")
	}
}

func TestCursorUsageDuplicateContinuationCannotClearRunningNonce(t *testing.T) {
	client, id, _ := newCursorUsageFixtureClient(t, agentclient.HostCallbacks{})
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	run, err := client.ReservePrompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}, agentclient.PromptOptions{BeforeSubmit: func(context.Context, agentclient.PromptSubmission) error { close(started); <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan agentclient.PromptResult, 1)
	go func() { finished <- run() }()
	<-started
	duplicate := run()
	if duplicate.Receipt.OperationError == nil {
		t.Fatal("duplicate continuation was accepted")
	}
	client.mu.Lock()
	nonce := client.usage.nonces[id]
	client.mu.Unlock()
	if nonce == "" {
		t.Fatal("duplicate continuation cleared running nonce")
	}
	close(release)
	result := <-finished
	if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted {
		t.Fatalf("running input=%+v", result)
	}
	client.mu.Lock()
	nonce = client.usage.nonces[id]
	client.mu.Unlock()
	if nonce != "" {
		t.Fatal("completed continuation retained nonce")
	}
}

func TestCursorUsageMissingJournalPreservesACPStreaming(t *testing.T) {
	var chunks []string
	client, id, trace := newCursorUsageFixtureClient(t, agentclient.HostCallbacks{Observe: func(_ context.Context, _ agentclient.Scope, n acp.SessionNotification) error {
		if chunk := n.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			chunks = append(chunks, chunk.Content.Text.Text)
		}
		return nil
	}})
	for _, text := range []string{"first", "second"} {
		run, err := client.ReservePrompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(text)}}, agentclient.PromptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		result := run()
		if result.Outcome == nil || result.Outcome.Turn.Status != acpmeta.NativeTurnCompleted || result.Outcome.Turn.Usage != nil || result.Receipt.OperationError != nil {
			t.Fatalf("missing usage changed native result: %+v", result)
		}
	}
	if strings.Join(chunks, " ") != "one two three one two three" {
		t.Fatalf("ACP deltas=%v", chunks)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	nonces := strings.Fields(string(data))
	if len(nonces) != 2 || nonces[0] == nonces[1] {
		t.Fatalf("prompt nonces=%v", nonces)
	}
	if _, ok := any(client).(agentclient.NativeOutcomeReader); ok {
		t.Fatal("usage hook added a second execution outcome owner")
	}
}

func TestCursorUsageClientFreezesExecutableAndDoesNotCreateDirectory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(t.TempDir(), "cursor-agent")
	if err = os.Symlink(executable, launcher); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	client, err := NewClient(context.Background(), Options{Executable: launcher, UsageHook: true}, agentclient.HostCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err = os.Stat(client.usage.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor materialized hook resources")
	}
	if err = os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(t.TempDir(), "updated"), launcher); err != nil {
		t.Fatal(err)
	}
	if client.opts.Executable != target || client.Executable() != target {
		t.Fatal("launcher retarget changed frozen executable")
	}
}

func TestCursorUsageCleanupRemovesOnlyReservedNonceFiles(t *testing.T) {
	client, id, _ := newCursorUsageFixtureClient(t, agentclient.HostCallbacks{})
	run, err := client.ReservePrompt(context.Background(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("hello")}}, agentclient.PromptOptions{BeforeSubmit: func(context.Context, agentclient.PromptSubmission) error { return errors.New("admission denied") }})
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	nonce := client.usage.nonces[id]
	client.mu.Unlock()
	current := filepath.Join(client.usage.directory, nonce+"-run.json")
	health := filepath.Join(client.usage.directory, "health.json")
	other := filepath.Join(client.usage.directory, newCursorUsageNonce()+"-run.json")
	for _, path := range []string{current, health, other} {
		if err = os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_ = run()
	if _, err = os.Stat(current); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed input retained own nonce file")
	}
	for _, path := range []string{health, other} {
		if _, err = os.Stat(path); err != nil {
			t.Fatal("cleanup removed unrelated usage evidence")
		}
	}
}

func TestCursorUsageCloseRetainsDirectoryUntilCleanupCanFinish(t *testing.T) {
	client, _, _ := newCursorUsageFixtureClient(t, agentclient.HostCallbacks{})
	usage := client.usage
	usage.directoryMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := client.Close(ctx)
	usage.directoryMu.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("incomplete metrics cleanup=%v", err)
	}
	if client.usage != usage {
		t.Fatal("incomplete cleanup lost owner")
	}
	if _, err = os.Stat(usage.directory); err != nil {
		t.Fatal("directory removed before cleanup ownership released")
	}
	if err = client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(usage.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cleanup retry retained private directory")
	}
}
