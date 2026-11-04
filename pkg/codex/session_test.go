package codex

import (
	"errors"
	"testing"
)

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
	if _, installed := store.install("thread-1", "/tmp", newGeneration, nil, terminalOutputModeDelta); !installed {
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
	if _, installed := store.install("thread-1", "/tmp", 0, nil, terminalOutputModeDelta); installed {
		t.Fatal("没有 beginOpen 的零 generation 被错误安装")
	}
}

// TestSessionStoreWithCurrentLinearizesConfigurationAndClose 验证配置变更与 close 共享同一线性化顺序。
func TestSessionStoreWithCurrentLinearizesConfigurationAndClose(t *testing.T) {
	t.Parallel()
	store := newSessionStore()
	generation, err := store.beginOpen("thread-config")
	if err != nil {
		t.Fatalf("开始 session 失败: %v", err)
	}
	if _, installed := store.install("thread-config", "/tmp", generation, nil, terminalOutputModeDelta); !installed {
		t.Fatal("安装 session 失败")
	}
	mutationEntered := make(chan struct{})
	releaseMutation := make(chan struct{})
	events := make(chan string, 2)
	mutationResult := make(chan error, 1)
	go func() {
		mutationResult <- store.withCurrent("thread-config", func(*sessionState) error {
			close(mutationEntered)
			<-releaseMutation
			events <- "mutation"
			return nil
		})
	}()
	<-mutationEntered
	closeResult := make(chan struct{})
	go func() {
		store.beginClose("thread-config")
		events <- "close"
		close(closeResult)
	}()
	close(releaseMutation)
	if err = <-mutationResult; err != nil {
		t.Fatalf("当前 session 配置变更失败: %v", err)
	}
	<-closeResult
	if first, second := <-events, <-events; first != "mutation" || second != "close" {
		t.Fatalf("线性化顺序为 %q → %q", first, second)
	}
	if err = store.withCurrent("thread-config", func(*sessionState) error { return nil }); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("close 后配置错误为 %v，期望 ErrSessionNotFound", err)
	}
}

// TestSessionStoreRejectsConcurrentOpen prevents two attempts from owning one native subscription.
func TestSessionStoreRejectsConcurrentOpen(t *testing.T) {
	store := newSessionStore()
	generation, err := store.beginOpen("thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.beginOpen("thread"); err == nil {
		t.Fatal("concurrent open admitted")
	}
	if !store.openCanProceed("thread", generation) {
		t.Fatal("concurrent open invalidated the first subscription")
	}
}

// TestSessionStoreRejectsOpeningInstalledThread protects its active lane and history owner.
func TestSessionStoreRejectsOpeningInstalledThread(t *testing.T) {
	store := newSessionStore()
	generation, err := store.beginOpen("thread")
	if err != nil {
		t.Fatal(err)
	}
	state, ok := store.install("thread", "/tmp", generation, nil, terminalOutputModeDelta)
	if !ok {
		t.Fatal("failed to install session")
	}
	if _, err := store.beginOpen("thread"); err == nil {
		t.Fatal("installed native thread can be replaced")
	}
	if !store.isCurrent(state) {
		t.Fatal("existing thread owner changed")
	}
}
