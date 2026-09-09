package cursor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHookLeasePreservesConcurrentChanges 验证退出只删除本次登记，保留原有 Hook 及用户同期增加的条目。
func TestHookLeasePreservesConcurrentChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".cursor", "hooks.json")
	original := []byte("{\n  \"version\": 1, \"hooks\": {\"stop\":[{\"command\":\"user-original\"}]}\n}\n")
	if err := os.WriteFile(path, original, 0640); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"hooks":{"stop":[{"command":"managed-session"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := installHookLease(context.Background(), root, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	if string(restored) != string(original) {
		t.Fatal("original formatting was lost")
	}
	lease, err = installHookLease(context.Background(), root, config)
	if err != nil {
		t.Fatal(err)
	}
	fields, hooks, err := decodeProjectHooks(lease.installed)
	if err != nil {
		t.Fatal(err)
	}
	hooks["stop"] = append(hooks["stop"], json.RawMessage(`{"command":"user-added"}`))
	fields["hooks"], _ = json.Marshal(hooks)
	edited, _ := json.Marshal(fields)
	if err = os.WriteFile(path, edited, 0640); err != nil {
		t.Fatal(err)
	}
	if err = lease.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	_, hooks, err = decodeProjectHooks(data)
	if err != nil || len(hooks["stop"]) != 2 {
		t.Fatalf("user changes lost: %s %v", data, err)
	}
	if err = lease.close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestHookLeasesShareWorkspace 验证两个适配会话关闭顺序不同也不会撤销彼此登记。
func TestHookLeasesShareWorkspace(t *testing.T) {
	root := t.TempDir()
	configRoot := t.TempDir()
	leases := []*hookLease{}
	for _, name := range []string{"first", "second"} {
		config := filepath.Join(configRoot, name+".json")
		data, _ := json.Marshal(map[string]any{"version": 1, "hooks": map[string]any{"stop": []any{map[string]any{"command": name}}}})
		if err := os.WriteFile(config, data, 0600); err != nil {
			t.Fatal(err)
		}
		lease, err := installHookLease(context.Background(), root, config)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
	}
	if err := leases[0].close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(leases[1].path)
	_, hooks, err := decodeProjectHooks(data)
	if err != nil || len(hooks["stop"]) != 1 {
		t.Fatalf("second lease lost: %s %v", data, err)
	}
	if err = leases[1].close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 第二个租约开始时已有配置，清理后允许保留不含适配命令的空 Hook 文件。
	if data, err = os.ReadFile(leases[1].path); err == nil {
		_, hooks, err = decodeProjectHooks(data)
		if err != nil || len(hooks) != 0 {
			t.Fatalf("stale hook: %s %v", data, err)
		}
	}
}
