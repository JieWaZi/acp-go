package cursor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestHookTransport 验证实际 Node 传输只保留官方计量字段，且读过的事件不会再次返回。
func TestHookTransport(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable for transport test")
	}
	root := t.TempDir()
	script := filepath.Join(root, "hook.cjs")
	if err = os.WriteFile(script, []byte(hookTransport), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(context.Background(), node, script, root)
	command.Stdin = strings.NewReader(`{"hook_event_name":"stop","conversation_id":"one","generation_id":"g1","status":"completed","input_tokens":100,"output_tokens":5,"cache_read_tokens":70,"cache_write_tokens":10,"user_email":"private@example.test","prompt":"private prompt"}`)
	if data, err := command.CombinedOutput(); err != nil || string(data) != "{}" {
		t.Fatalf("hook: %v %s", err, data)
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(root, entry.Name()))
			if strings.Contains(string(data), "private") {
				t.Fatal("hook retained private fields")
			}
		}
	}
	events, err := readHooks(context.Background(), root)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	usage := events[0].usage()
	if usage.InputTokens != 20 || usage.OutputTokens != 5 || usage.TotalTokens != 105 || *usage.CachedWriteTokens != 10 {
		t.Fatalf("usage=%+v", usage)
	}
	events, err = readHooks(context.Background(), root)
	if err != nil || len(events) != 0 {
		t.Fatalf("events replayed: %+v %v", events, err)
	}
}

// TestHookUsageUnknownAndZero 区分上游未提供、明确零值与无效计量。
func TestHookUsageUnknownAndZero(t *testing.T) {
	var event hookEvent
	if err := json.Unmarshal([]byte(`{"hook_event_name":"stop","conversation_id":"s","generation_id":"g","status":"completed"}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.usage() != nil {
		t.Fatal("unknown usage became zero")
	}
	event.Input = acp.Ptr(0)
	event.Output = acp.Ptr(0)
	if event.usage() == nil || event.usage().TotalTokens != 0 {
		t.Fatal("explicit zero lost")
	}
	event.CacheRead = -1
	if event.usage() != nil {
		t.Fatal("negative usage accepted")
	}
}
