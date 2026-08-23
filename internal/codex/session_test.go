package codex

import "testing"

// TestSessionStoreStaleOpenCannotCleanNewInstallation 验证旧 generation 不会因 map 零值误认仍拥有订阅。
func TestSessionStoreStaleOpenCannotCleanNewInstallation(t *testing.T) {
	t.Parallel()
	store := newSessionStore()
	oldGeneration, err := store.beginOpen("thread-1")
	if err != nil {
		t.Fatalf("开始旧 open 失败: %v", err)
	}
	store.beginClose("thread-1")
	store.endClose("thread-1")
	newGeneration, err := store.beginOpen("thread-1")
	if err != nil {
		t.Fatalf("开始新 open 失败: %v", err)
	}
	if _, installed := store.install("thread-1", "/tmp", newGeneration); !installed {
		t.Fatal("新 generation 未安装")
	}
	if store.beginStaleCleanup("thread-1", oldGeneration) {
		t.Fatal("旧 open 在新状态安装后仍获得 unsubscribe 所有权")
	}
}

// TestSessionStoreInstallRequiresMatchingOpenIdentity 验证 generation 零值不能绕过 beginOpen。
func TestSessionStoreInstallRequiresMatchingOpenIdentity(t *testing.T) {
	t.Parallel()
	store := newSessionStore()
	if _, installed := store.install("thread-1", "/tmp", 0); installed {
		t.Fatal("没有 beginOpen 的零 generation 被错误安装")
	}
}
