package gemini

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestTranscriptIgnoresResetContext 验证官方 session_context 检查点不覆盖真实可恢复内容。
func TestTranscriptIgnoresResetContext(t *testing.T) {
	data := []byte("{\"sessionId\":\"s\",\"projectHash\":\"p\"}\n{\"id\":\"user\",\"type\":\"user\",\"content\":\"actual\"}\n{\"$set\":{\"messages\":[{\"id\":\"context\",\"type\":\"user\",\"content\":[{\"text\":\"<session_context>init</session_context>\"}]}]}}\n")
	if _, match, err := inspectTranscript("fixture.jsonl", data, "s"); err != nil || match {
		t.Fatalf("startup reset counted as history: match=%v err=%v", match, err)
	}
}

// TestFailedReplacementPreservesFutureHistory 验证失败重载回滚官方覆盖，再次写入不会截断旧历史。
func TestFailedReplacementPreservesFutureHistory(t *testing.T) {
	agent, client := startSessionAgent(t, Config{Environment: []string{"GEMINI_CLI_HOME=" + t.TempDir()}})
	session := newFixtureSession(t, agent)
	prompt := func(text string) {
		t.Helper()
		if _, err := agent.Prompt(context.Background(), acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)}}); err != nil {
			t.Fatal(err)
		}
		<-client.updates
	}
	prompt("before failed change")
	before := captureSession(t, agent, session.SessionId)
	path := filepath.Join(before["profile"].(string), "tmp", "project", "chats", "session-current-"+before["sessionId"].(string)+".jsonl")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: "model", Value: "fail-model"}})
	if err == nil {
		t.Fatal("failed model accepted")
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatal("failed replacement changed original transcript bytes")
	}
	prompt("after failed change")
	configureSession(t, agent, session.SessionId, "reasoning", "low")
	history := captureSession(t, agent, session.SessionId)["history"].(string)
	if !strings.Contains(history, "before failed change") || !strings.Contains(history, "after failed change") {
		t.Fatalf("rollback lost history: %s", history)
	}
}

// TestStateRejectsSymlinkCredential 验证受管凭据不能通过链接覆盖其他文件。
func TestStateRejectsSymlinkCredential(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".gemini", "oauth_creds.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := thinkingEnvironment([]string{"GEMINI_CLI_HOME=" + root}); err == nil {
		t.Fatal("unsafe credential symlink accepted")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "original" {
		t.Fatal("external credential modified")
	}
}

// TestStateWriteRejectsReplacedParent 验证状态子目录被替换成链接后不会写入外部目录。
func TestStateWriteRejectsReplacedParent(t *testing.T) {
	state, err := newProfileState([]string{"HOME=" + t.TempDir()}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(state.root, "acp-go-sessions")
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, directory); err != nil {
		t.Fatal(err)
	}
	if err := state.saveRecord("public", sessionRecord{NativeID: "native", Cwd: "/fixture"}); err == nil {
		t.Fatal("state write followed replaced parent symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("outside directory was written")
	}
}
