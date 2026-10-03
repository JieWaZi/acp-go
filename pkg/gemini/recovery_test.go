package gemini

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestCanceledAdmissionRemainsEmpty 验证尚未发往原生的取消不会阻塞空会话配置和重启恢复。
func TestCanceledAdmissionRemainsEmpty(t *testing.T) {
	for _, restartFirst := range []bool{false, true} {
		name := "configure_first"
		if restartFirst {
			name = "restart_first"
		}
		t.Run(name, func(t *testing.T) {
			config := Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}}
			agent, _ := startSessionAgent(t, config)
			created := newFixtureSession(t, agent)
			session, _ := agent.findSession(created.SessionId)
			before := captureSession(t, agent, created.SessionId)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			agent.profile.mutex.Lock()
			done := make(chan error, 1)
			go func() {
				_, err := agent.Prompt(ctx, acp.PromptRequest{
					SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("must not reach native")},
				})
				done <- err
			}()
			admitted := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				session.mutex.Lock()
				admitted = session.pending == 1
				session.mutex.Unlock()
				if admitted {
					break
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			agent.profile.mutex.Unlock()
			if !admitted {
				t.Fatal("prompt was not admitted")
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled prompt: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("canceled prompt blocked")
			}
			if got := captureSession(t, agent, created.SessionId)["history"]; got != "" {
				t.Fatalf("pre-native cancellation executed: %v", got)
			}
			record, err := agent.profile.loadRecord(created.SessionId, "")
			if err != nil {
				t.Fatal(err)
			}
			if record.HasHistory {
				t.Error("pre-native cancellation persisted resumable history")
			}
			if !restartFirst {
				configureSession(t, agent, created.SessionId, "reasoning", "low")
			}
			if err := agent.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			next, _ := startSessionAgent(t, config)
			_, err = next.ResumeSession(context.Background(), acp.ResumeSessionRequest{
				SessionId: created.SessionId, Cwd: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			configureSession(t, next, created.SessionId, "reasoning", "medium")
			after := captureSession(t, next, created.SessionId)
			if after["sessionId"] == before["sessionId"] || after["history"] != "" {
				t.Fatalf("empty session was not recreated: %v", after)
			}
		})
	}
}

// TestPartialPromptHistorySurvives 验证取消和失败保留真正写入的部分历史，包括历史标记尚未提交的重启。
func TestPartialPromptHistorySurvives(t *testing.T) {
	for _, text := range []string{"partial-wait", "partial-error", "uncommitted-history"} {
		t.Run(text, func(t *testing.T) {
			config := Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}}
			agent, client := startSessionAgent(t, config)
			created := newFixtureSession(t, agent)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := agent.Prompt(ctx, acp.PromptRequest{
					SessionId: created.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)},
				})
				done <- err
			}()
			select {
			case <-client.updates:
			case <-time.After(3 * time.Second):
				t.Fatal("native did not record partial turn")
			}
			if text == "partial-wait" {
				cancel()
			}
			select {
			case err := <-done:
				if text != "uncommitted-history" && err == nil {
					t.Fatal("partial prompt should fail or cancel")
				}
				if text == "uncommitted-history" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("partial prompt blocked")
			}
			record, err := agent.profile.loadRecord(created.SessionId, "")
			if err != nil {
				t.Fatal(err)
			}
			if !record.HasHistory {
				t.Fatal("genuine partial history was not committed")
			}
			if text == "uncommitted-history" {
				// 模拟原生日志已落盘、适配器尚未提交标记便退出的窗口。
				record.HasHistory = false
				if err := agent.profile.saveRecord(created.SessionId, record); err != nil {
					t.Fatal(err)
				}
			} else {
				configureSession(t, agent, created.SessionId, "reasoning", "low")
			}
			if err := agent.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			next, _ := startSessionAgent(t, config)
			_, err = next.ResumeSession(context.Background(), acp.ResumeSessionRequest{
				SessionId: created.SessionId, Cwd: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			history := captureSession(t, next, created.SessionId)["history"].(string)
			if !strings.Contains(history, text) {
				t.Fatalf("lost recorded partial history: %s", history)
			}
		})
	}
}

// TestFailedLoadLeavesSessionUnavailable 验证映射及原生准备失败后所有操作安全拒绝，并允许修复后重新加载。
func TestFailedLoadLeavesSessionUnavailable(t *testing.T) {
	for _, kind := range []string{"malformed", "invalid", "directory", "symlink", "unreadable", "native_failure"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "unreadable" && os.Getuid() == 0 {
				t.Skip("root can read mode 0000")
			}
			agent, _ := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
			id := acp.SessionId("failed-load")
			cwd := t.TempDir()
			path := filepath.Join(agent.profile.root, recordName(id))
			record := sessionRecord{NativeID: "old-native", Cwd: cwd, Model: "fail-model", Reasoning: "default"}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "native_failure", "unreadable":
				if err := agent.profile.saveRecord(id, record); err != nil {
					t.Fatal(err)
				}
				if kind == "unreadable" {
					if err := os.Chmod(path, 0000); err != nil {
						t.Fatal(err)
					}
				}
			default:
				content := "{broken"
				if kind == "invalid" {
					content = "{}"
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := agent.LoadSession(context.Background(), acp.LoadSessionRequest{SessionId: id, Cwd: cwd}); err == nil {
				t.Fatal("invalid restore succeeded")
			}
			if session, err := agent.findSession(id); err == nil {
				session.mutex.Lock()
				closed := session.closed
				session.mutex.Unlock()
				if !closed {
					t.Error("failed load published an open session")
				}
			}
			assertSessionUnavailable(t, agent, id)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			record.Model = "gemini-3.8-flash"
			if err := agent.profile.saveRecord(id, record); err != nil {
				t.Fatal(err)
			}
			_, err := agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{SessionId: id, Cwd: cwd})
			if err != nil {
				t.Fatal(err)
			}
			_, err = agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: "default"})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestUnavailableChildOperations 验证不完整内部状态也不能在公开操作中导致空指针崩溃。
func TestUnavailableChildOperations(t *testing.T) {
	agent, _ := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	id := acp.SessionId("unavailable")
	agent.mutex.Lock()
	agent.sessions[id] = &ownedSession{}
	agent.mutex.Unlock()
	assertSessionUnavailable(t, agent, id)
}

// assertSessionUnavailable 对所有相关入口验证失败后的会话不可执行，关闭保持幂等安全。
func assertSessionUnavailable(t *testing.T, agent *Agent, id acp.SessionId) {
	t.Helper()
	operations := map[string]func() error{
		"mode": func() error {
			_, err := agent.SetSessionMode(context.Background(), acp.SetSessionModeRequest{SessionId: id, ModeId: "default"})
			return err
		},
		"prompt": func() error {
			_, err := agent.Prompt(context.Background(), acp.PromptRequest{
				SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("must not execute")},
			})
			return err
		},
		"cancel": func() error { return agent.Cancel(context.Background(), acp.CancelNotification{SessionId: id}) },
		"config": func() error {
			_, err := agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
				ValueId: &acp.SetSessionConfigOptionValueId{SessionId: id, ConfigId: "model", Value: "gemini-3.8-flash"},
			})
			return err
		},
		"extension": func() error {
			_, err := agent.CallNative(context.Background(), "_capture", map[string]any{"sessionId": id})
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if failure := recover(); failure != nil {
					t.Errorf("unavailable session panicked: %v", failure)
				}
			}()
			if err := operation(); err == nil {
				t.Error("unavailable session accepted operation")
			}
		})
	}
	if _, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: id}); err != nil {
		// 未发布的会话可以按协议拒绝关闭；已关闭的占位会话可以幂等返回成功。
		if _, findErr := agent.findSession(id); findErr == nil {
			t.Errorf("close unavailable session: %v", err)
		}
	}
}
