//go:build unix

package nativeacp_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/nativeacp"
)

// TestNativeProcessTreeClose 验证退出适配器时同时终止它启动的执行进程。
func TestNativeProcessTreeClose(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "child-pid")
	agent, err := nativeacp.NewAgent(context.Background(), nativeacp.Config{Command: binary, Args: []string{"-test.run=^TestNativeDescendantProcess$"}, Environment: append(os.Environ(), "NATIVE_ACP_DESCENDANT="+marker), Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer agent.Close(ctx)
	var pid int
	for pid == 0 && ctx.Err() == nil {
		data, _ := os.ReadFile(marker)
		pid, _ = strconv.Atoi(string(data))
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("descendant did not start")
	}
	if err := agent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("adapter descendant survived Close")
}

// TestNativeDescendantProcess 模拟 Pi 适配器持有独立执行进程的生命周期。
func TestNativeDescendantProcess(t *testing.T) {
	marker := os.Getenv("NATIVE_ACP_DESCENDANT")
	if marker == "" {
		return
	}
	child := exec.Command("/bin/sleep", "60")
	if err := child.Start(); err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile(marker, []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		_ = child.Process.Kill()
		os.Exit(1)
	}
	_ = child.Wait()
	os.Exit(0)
}
