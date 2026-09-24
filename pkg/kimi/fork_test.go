package kimi

import (
	"context"
	"encoding/json"
	"github.com/JieWaZi/acp-go/internal/sessionfiles"
	acp "github.com/coder/acp-go-sdk"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKimiForkRebindPreservesFullNativeState 验证原生 wire、压缩和子 Agent 状态独立复制。
func TestKimiForkRebindPreservesFullNativeState(t *testing.T) {
	source, target, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	state := `{"id":"intermediate","cwd":"old","agents":{"main":{"homedir":"old/agents/main"}},"forkedFrom":{"sessionId":"parent"}}`
	if err := os.WriteFile(filepath.Join(source, "state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "agents", "main"), 0700); err != nil {
		t.Fatal(err)
	}
	wire := []byte("native-message-before\ncompaction-context\ntool-result\n" + `{"type":"runtime.set_binding","workspaceId":"source-workspace","runtimeId":"acp:intermediate","agentId":"main","time":1}` + "\n")
	if err := os.MkdirAll(filepath.Join(target, "agents", "main"), 0700); err != nil {
		t.Fatal(err)
	}
	binding := `{"type":"runtime.set_binding","workspaceId":"target-workspace","runtimeId":"acp:child","agentId":"main","time":2}` + "\n"
	if err := os.WriteFile(filepath.Join(target, "agents", "main", "wire.jsonl"), []byte(binding), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "agents", "main", "wire.jsonl"), wire, 0600); err != nil {
		t.Fatal(err)
	}
	if err := rebindNativeFork(source, target, "child", cwd); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(target, "state.json"))
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "child" || metadata["cwd"] != cwd {
		t.Fatal(metadata)
	}
	agents := metadata["agents"].(map[string]any)
	if agents["main"].(map[string]any)["homedir"] != filepath.Join(target, "agents", "main") {
		t.Fatal(metadata)
	}
	unchanged, _ := os.ReadFile(filepath.Join(source, "state.json"))
	if string(unchanged) != state {
		t.Fatal("source metadata changed")
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(target, "agents", "main", "wire.jsonl"))
	if err != nil || !strings.HasPrefix(string(content), string(wire)) || !strings.Contains(string(content[len(wire):]), `"workspaceId":"target-workspace"`) {
		t.Fatalf("child depends on parent: %q %v", content, err)
	}
}

// TestKimiForkRebindKeepsAgentRuntime 验证子 Agent 的独立运行时绑定不被 main 覆盖。
func TestKimiForkRebindKeepsAgentRuntime(t *testing.T) {
	copied, target := t.TempDir(), t.TempDir()
	for _, agentID := range []string{"main", "researcher"} {
		if err := os.MkdirAll(filepath.Join(copied, "agents", agentID), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(target, "agents", "main"), 0700); err != nil {
		t.Fatal(err)
	}
	main := []byte(`{"type":"runtime.set_binding","workspaceId":"old","runtimeId":"local","agentId":"main"}` + "\n")
	sub := []byte(`{"type":"runtime.set_binding","workspaceId":"old","runtimeId":"remote-research","agentId":"researcher"}` + "\n")
	targetBinding := []byte(`{"type":"runtime.set_binding","workspaceId":"new","runtimeId":"local","agentId":"main"}` + "\n")
	for path, content := range map[string][]byte{
		filepath.Join(copied, "agents", "main", "wire.jsonl"):       main,
		filepath.Join(copied, "agents", "researcher", "wire.jsonl"): sub,
		filepath.Join(target, "agents", "main", "wire.jsonl"):       targetBinding,
	} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	agents := map[string]map[string]json.RawMessage{"main": {}, "researcher": {}}
	if err := rebindForkRuntime(copied, target, agents); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(copied, "agents", "researcher", "wire.jsonl"))
	if err != nil || !strings.Contains(string(content[len(sub):]), `"runtimeId":"remote-research"`) || !strings.Contains(string(content[len(sub):]), `"workspaceId":"new"`) {
		t.Fatalf("sub-agent runtime binding changed: %s %v", content, err)
	}
}

// TestKimiNativeForkRebindAndColdContinuation 通过原生 ACP 测试进程验证完整跨工作区生命周期。
func TestKimiNativeForkRebindAndColdContinuation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home, sourceCwd, targetCwd := t.TempDir(), t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config := Config{KimiPath: binary, PrefixArgs: []string{"-test.run=^TestKimiForkACPProcess$", "--"}, Environment: append(os.Environ(), "KIMI_FORK_FIXTURE=1", "KIMI_CODE_HOME="+home), StateDirectory: t.TempDir(), WorkingDirectory: sourceCwd, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	if _, err = a.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	parent, err := a.NewSession(ctx, acp.NewSessionRequest{Cwd: sourceCwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Prompt(ctx, acp.PromptRequest{SessionId: parent.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("before-fork")}}); err != nil {
		t.Fatal(err)
	}
	source := indexedWirePath(home, parent.SessionId, sourceCwd)
	original, _ := os.ReadFile(source)
	request := acp.UnstableForkSessionRequest{SessionId: parent.SessionId, Cwd: targetCwd, Meta: map[string]any{"sourceCwd": sourceCwd, "forkReceiptDirectory": t.TempDir()}}
	child, err := a.UnstableForkSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if child.SessionId == parent.SessionId {
		t.Fatal("identity reused")
	}
	target := indexedWirePath(home, child.SessionId, targetCwd)
	inherited, _ := os.ReadFile(target)
	if !strings.Contains(string(inherited), "before-fork") {
		t.Fatal("native context lost")
	}
	if err = a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cold, err := NewAgent(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close(context.Background())
	if _, err = cold.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	request.Meta["reconcileOnly"] = true
	reconciled, err := cold.UnstableForkSession(ctx, request)
	if err != nil || reconciled.SessionId != child.SessionId {
		t.Fatalf("reconcile: %+v %v", reconciled, err)
	}
	if _, err = cold.Prompt(ctx, acp.PromptRequest{SessionId: child.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("child-only")}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "before-fork") || !strings.Contains(string(data), "child-only") {
		t.Fatal("cold continuation lost context")
	}
	unchanged, _ := os.ReadFile(source)
	if string(unchanged) != string(original) {
		t.Fatal("parent changed")
	}
	// 收据存在但末尾工作区绑定损坏时，不能发布无法原生恢复的分支。
	broken := append(data, []byte(`{"type":"runtime.set_binding","workspaceId":"wrong-workspace","runtimeId":"local","agentId":"main"}`+"\n")...)
	if err := os.WriteFile(target, broken, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cold.UnstableForkSession(ctx, request); err == nil {
		t.Fatal("damaged native fork reconciled as successful")
	}
}

// TestKimiForkACPProcess 模拟上游忽略 fork.cwd、通过 new 建立目标索引的真实协议边界。
func TestKimiForkACPProcess(t *testing.T) {
	if os.Getenv("KIMI_FORK_FIXTURE") != "1" {
		return
	}
	home := os.Getenv("KIMI_CODE_HOME")
	writeSession := func(id, cwd, copyID string) error {
		directory := filepath.Join(home, "sessions", "workspace", id)
		if copyID != "" {
			if err := sessionfiles.CopyTree(filepath.Join(home, "sessions", "workspace", copyID), directory); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Join(directory, "agents", "main"), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(directory, "agents", "main", "wire.jsonl"), nil, 0600); err != nil {
				return err
			}
		}
		metadata, _ := json.Marshal(map[string]any{"id": id, "cwd": cwd, "agents": map[string]any{"main": map[string]any{"homedir": filepath.Join(directory, "agents", "main")}}})
		if err := os.WriteFile(filepath.Join(directory, "state.json"), metadata, 0600); err != nil {
			return err
		}
		binding, _ := json.Marshal(map[string]any{"type": "runtime.set_binding", "workspaceId": cwd, "runtimeId": "acp:" + id, "agentId": "main"})
		file, err := os.OpenFile(filepath.Join(directory, "agents", "main", "wire.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(append(binding, '\n'))
		_ = file.Close()
		if err != nil {
			return err
		}
		index, err := os.OpenFile(filepath.Join(home, "session_index.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer index.Close()
		return json.NewEncoder(index).Encode(map[string]any{"sessionDir": directory, "sessionId": id, "workDir": cwd})
	}
	connection := acp.NewConnection(func(_ context.Context, method string, data json.RawMessage) (any, *acp.RequestError) {
		var request map[string]any
		_ = json.Unmarshal(data, &request)
		fail := func(err error) (any, *acp.RequestError) {
			return nil, acp.NewInternalError(map[string]any{"message": err.Error()})
		}
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "Kimi Code CLI", "version": "2.0.2"}, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"fork": map[string]any{}, "close": map[string]any{}, "resume": map[string]any{}}}}, nil
		case "session/new":
			id := "parent"
			if _, err := os.Stat(filepath.Join(home, "session_index.jsonl")); err == nil {
				id = "child"
			}
			if err := writeSession(id, request["cwd"].(string), ""); err != nil {
				return fail(err)
			}
			return map[string]any{"sessionId": id}, nil
		case "session/fork":
			id := request["sessionId"].(string)
			raw, err := os.ReadFile(filepath.Join(home, "sessions", "workspace", id, "state.json"))
			if err != nil {
				return fail(err)
			}
			var metadata map[string]any
			_ = json.Unmarshal(raw, &metadata)
			if err = writeSession("intermediate", metadata["cwd"].(string), id); err != nil {
				return fail(err)
			}
			return map[string]any{"sessionId": "intermediate"}, nil
		case "session/close":
			return map[string]any{}, nil
		case "session/resume":
			raw, err := os.ReadFile(filepath.Join(home, "sessions", "workspace", request["sessionId"].(string), "state.json"))
			if err != nil {
				return fail(err)
			}
			var metadata map[string]any
			_ = json.Unmarshal(raw, &metadata)
			if metadata["cwd"] != request["cwd"] {
				return nil, acp.NewInvalidParams(nil)
			}
			wire, err := os.ReadFile(filepath.Join(home, "sessions", "workspace", request["sessionId"].(string), "agents", "main", "wire.jsonl"))
			if err != nil {
				return fail(err)
			}
			var binding map[string]any
			for _, line := range strings.Split(string(wire), "\n") {
				var record map[string]any
				if json.Unmarshal([]byte(line), &record) == nil && record["type"] == "runtime.set_binding" {
					binding = record
				}
			}
			if binding["workspaceId"] != request["cwd"] || binding["runtimeId"] != "acp:"+request["sessionId"].(string) {
				return nil, acp.NewInvalidParams(map[string]any{"message": "runtime binding mismatch"})
			}
			return map[string]any{}, nil
		case "session/prompt":
			path := filepath.Join(home, "sessions", "workspace", request["sessionId"].(string), "agents", "main", "wire.jsonl")
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return fail(err)
			}
			_, err = file.Write(append(data, '\n'))
			_ = file.Close()
			if err != nil {
				return fail(err)
			}
			return map[string]any{"stopReason": "end_turn"}, nil
		default:
			return nil, acp.NewMethodNotFound(method)
		}
	}, os.Stdout, os.Stdin)
	<-connection.Done()
	os.Exit(0)
}
