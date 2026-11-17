//go:build darwin || linux

package agentclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// processReleaseRecord identifies an actual child that has acquired its kernel lock.
type processReleaseRecord struct {
	// PID is the real process holding the file description.
	PID int
	// Group is the actual process group inherited from the native leader.
	Group int
}

// TestProcessReleaseAcceptanceChild supplies real descendants with independent stdio and kernel locks.
func TestProcessReleaseAcceptanceChild(t *testing.T) {
	role := os.Getenv("ACP_RELEASE_ACCEPTANCE_ROLE")
	if role == "" {
		return
	}
	directory := os.Getenv("ACP_RELEASE_ACCEPTANCE_DIRECTORY")
	if role == "descendant" {
		signal.Ignore(syscall.SIGTERM)
		index, err := strconv.Atoi(os.Getenv("ACP_RELEASE_ACCEPTANCE_INDEX"))
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("held-%02d.lock", index)), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		group, err := syscall.Getpgid(0)
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("held-%02d.json", index)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(out).Encode(processReleaseRecord{PID: os.Getpid(), Group: group}); err != nil {
			t.Fatal(err)
		}
		if err := out.Sync(); err != nil {
			t.Fatal(err)
		}
		_ = out.Close()
		deadline := time.Now().Add(90 * time.Second)
		for !processReleaseExists(filepath.Join(directory, "release-descendants")) {
			if time.Now().After(deadline) {
				os.Exit(72)
			}
			time.Sleep(time.Millisecond)
		}
		os.Exit(0)
	}
	if role != "leader" {
		t.Fatalf("unknown release fixture role %s", role)
	}
	if os.Getenv("ACP_RELEASE_ACCEPTANCE_EXIT") == "close" {
		signal.Ignore(syscall.SIGTERM)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for index := range 16 {
		command := exec.Command(executable, "-test.run=^TestProcessReleaseAcceptanceChild$", "-test.timeout=100s")
		command.Env = processReleaseEnvironment("descendant", directory, index)
		// No descendant shares the native leader's stdin/stdout/stderr. Draining those streams proves nothing about these locks.
		null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		command.Stdin, command.Stdout, command.Stderr = null, null, null
		if err := command.Start(); err != nil {
			_ = null.Close()
			t.Fatal(err)
		}
		_ = null.Close()
		go func() { _ = command.Wait() }()
		processReleasePoll(t, func() bool {
			return processReleaseExists(filepath.Join(directory, fmt.Sprintf("held-%02d.json", index)))
		})
	}
	fmt.Fprintln(os.Stdout, "all real descendants hold independent locks")
	deadline := time.Now().Add(90 * time.Second)
	for !processReleaseExists(filepath.Join(directory, "exit-leader")) {
		if time.Now().After(deadline) {
			os.Exit(73)
		}
		time.Sleep(time.Millisecond)
	}
	os.Exit(0)
}

func processReleaseEnvironment(role, directory string, index int) []string {
	result := []string{}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "ACP_RELEASE_ACCEPTANCE_") {
			result = append(result, item)
		}
	}
	return append(result, "ACP_RELEASE_ACCEPTANCE_ROLE="+role, "ACP_RELEASE_ACCEPTANCE_DIRECTORY="+directory, "ACP_RELEASE_ACCEPTANCE_INDEX="+strconv.Itoa(index))
}

func processReleaseExists(path string) bool { _, err := os.Stat(path); return err == nil }

func processReleaseDirectory(t *testing.T) string {
	t.Helper()
	output := os.Getenv("ACP_PROCESS_ACCEPTANCE_OUTPUT")
	if output == "" {
		return t.TempDir()
	}
	directory := filepath.Join(output, strings.ReplaceAll(t.Name(), "/", "_"))
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal("refusing to overwrite process acceptance evidence: ", err)
	}
	return directory
}

func processReleasePoll(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("real process release fixture timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func processReleaseLockHeld(t *testing.T, path string) bool {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	return false
}

// TestProcessReleaseAcceptanceRejectsLeaderOrStderrAsGroupProof checks real resources after the public Close contract returns.
func TestProcessReleaseAcceptanceRejectsLeaderOrStderrAsGroupProof(t *testing.T) {
	for _, mode := range []string{"leader-first", "close"} {
		t.Run(mode, func(t *testing.T) {
			directory := processReleaseDirectory(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			env := append(processReleaseEnvironment("leader", directory, 0), "ACP_RELEASE_ACCEPTANCE_EXIT="+mode)
			owner, err := NewProcess(ProcessSpec{Executable: executable, Args: []string{"-test.run=^TestProcessReleaseAcceptanceChild$", "-test.timeout=100s"}, Env: env})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(directory, "release-descendants"), nil, 0600)
				_ = os.WriteFile(filepath.Join(directory, "exit-leader"), nil, 0600)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := owner.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if err := owner.Start(ctx); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(owner.Stdout()).ReadString('\n')
			if err != nil || line != "all real descendants hold independent locks\n" {
				t.Fatalf("missing actual descendant admission: %q %v", line, err)
			}
			for index := range 16 {
				if !processReleaseLockHeld(t, filepath.Join(directory, fmt.Sprintf("held-%02d.lock", index))) {
					t.Fatalf("fixture did not hold lock %d before close", index)
				}
			}
			if mode == "leader-first" {
				if err := os.WriteFile(filepath.Join(directory, "exit-leader"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				// The leader must finish on its own before Close is allowed to request termination.
				select {
				case <-owner.Done():
				case <-ctx.Done():
					t.Fatal("leader did not naturally exit and release its group: ", ctx.Err())
				}
				exit, err := owner.Wait(ctx)
				if err != nil || exit.Code != 0 || exit.Err != nil {
					t.Fatalf("leader-first fixture did not exit naturally: %+v %v", exit, err)
				}
			}
			if err := owner.Close(ctx); err != nil {
				t.Fatal(err)
			}
			for index := range 16 {
				if processReleaseLockHeld(t, filepath.Join(directory, fmt.Sprintf("held-%02d.lock", index))) {
					t.Fatalf("Close reported release while independent descendant still holds lock %d; leader exit/stderr drain/kill request is not group release", index)
				}
			}
			guardianAcceptanceJSON(t, filepath.Join(directory, "result.json"), map[string]any{"Mode": mode, "RealIndependentDescendants": 16, "AllLocksBusyBeforeExit": true, "AllLocksReleasedAfterClose": true, "LeaderNaturalExitBeforeClose": mode == "leader-first"})
		})
	}
}
