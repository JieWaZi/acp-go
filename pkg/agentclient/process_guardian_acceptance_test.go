//go:build darwin || linux

package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// guardianAcceptanceFD identifies a real inherited kernel object, independent of descriptor reuse.
type guardianAcceptanceFD struct {
	// Name identifies the private endpoint or actual native descriptor.
	Name string
	// Device is the kernel device identity returned by fstat.
	Device uint64
	// Inode is the kernel object identity returned by fstat.
	Inode uint64
	// Mode distinguishes file and pipe object classes.
	Mode uint32
}

func guardianAcceptanceJSON(t *testing.T, path string, value any) {
	t.Helper()
	pending := path + ".pending"
	file, err := os.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	// A completed record becomes visible atomically, and a repeated admission cannot replace it.
	if err := os.Link(pending, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
}

func guardianAcceptanceRead(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func guardianAcceptanceIdentity(t *testing.T, fd int, name string) guardianAcceptanceFD {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		t.Fatal(err)
	}
	return guardianAcceptanceFD{Name: name, Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Mode: uint32(stat.Mode) & unix.S_IFMT}
}

func guardianAcceptanceLease(t *testing.T, directory string) *os.File {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(directory, "core.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return file
}

func guardianAcceptanceConfig(t *testing.T, directory string, lease *os.File) (*ProcessGuardian, <-chan struct{}) {
	t.Helper()
	duplicated := make(chan struct{})
	return &ProcessGuardian{Executable: os.Args[0], Args: []string{"-test.run=^TestProcessGuardianAcceptanceChild$", "--", "guardian-acceptance", directory}, DuplicateLease: func() (*os.File, error) {
		defer close(duplicated)
		fd, err := unix.FcntlInt(lease.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "guardian-acceptance-lease"), nil
	}}, duplicated
}

func guardianAcceptanceNativeSpec(directory string) ProcessSpec {
	return ProcessSpec{Executable: os.Args[0], Args: []string{"-test.run=^TestProcessGuardianAcceptanceChild$", "--", "native-acceptance", directory}}
}

// TestProcessGuardianAcceptanceChild dispatches before opening any store or acquiring the host lease.
func TestProcessGuardianAcceptanceChild(t *testing.T) {
	if len(os.Args) < 3 {
		return
	}
	role, directory := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	switch role {
	case "guardian-acceptance":
		identities := []guardianAcceptanceFD{}
		for index, name := range []string{"guardian-control-read", "guardian-status-write", "guardian-lease"} {
			identities = append(identities, guardianAcceptanceIdentity(t, index+3, name))
		}
		guardianAcceptanceJSON(t, filepath.Join(directory, "guardian-ready.json"), identities)
		processReleasePoll(t, func() bool { return processReleaseExists(filepath.Join(directory, "guardian-release")) })
		os.Exit(RunProcessGuardian())
	case "native-acceptance":
		file, err := os.OpenFile(filepath.Join(directory, "native.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		descriptors, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		identities := []guardianAcceptanceFD{}
		for _, entry := range descriptors {
			fd, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			var stat unix.Stat_t
			if unix.Fstat(fd, &stat) == nil {
				identities = append(identities, guardianAcceptanceIdentity(t, fd, entry.Name()))
			}
		}
		guardianAcceptanceJSON(t, filepath.Join(directory, "native-started.json"), map[string]any{"PID": os.Getpid(), "Descriptors": identities})
		// The native process never reads stdin. Only its independent owner can terminate this execution.
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		os.Exit(74)
	case "host-incomplete-acceptance", "host-complete-acceptance":
		lease := guardianAcceptanceLease(t, directory)
		config, _ := guardianAcceptanceConfig(t, directory, lease)
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		statusRead, statusWrite, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer statusRead.Close()
		command := exec.Command(config.Executable, config.Args...)
		command.ExtraFiles = []*os.File{read, statusWrite, lease}
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		_ = read.Close()
		_ = statusWrite.Close()
		owner, err := NewProcess(guardianAcceptanceNativeSpec(directory))
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(guardianLaunch{Executable: owner.spec.Executable, Args: owner.spec.Args, Env: owner.spec.Env, Dir: owner.spec.Dir})
		if err != nil {
			t.Fatal(err)
		}
		if role == "host-incomplete-acceptance" {
			// An incomplete actual JSON object must never admit the otherwise valid native executable.
			payload = payload[:len(payload)-1]
		} else {
			payload = append(payload, '\n')
		}
		// This raw wire fixture completes the real write before declaring its exact transfer boundary.
		if count, err := write.Write(payload); err != nil || count != len(payload) {
			t.Fatalf("actual launch transfer incomplete: %d/%d %v", count, len(payload), err)
		}
		processReleasePoll(t, func() bool { return processReleaseExists(filepath.Join(directory, "guardian-ready.json")) })
		guardianAcceptanceJSON(t, filepath.Join(directory, "host-ready.json"), map[string]any{"PID": os.Getpid(), "Role": role, "LaunchBytesTransferred": len(payload)})
		for {
			time.Sleep(time.Second)
		}
	}
}

// TestProcessGuardianAcceptanceHostLoss checks real SIGKILL on both sides of complete launch transfer.
func TestProcessGuardianAcceptanceHostLoss(t *testing.T) {
	for _, mode := range []string{"incomplete", "complete"} {
		t.Run(mode, func(t *testing.T) {
			directory := processReleaseDirectory(t)
			command := exec.Command(os.Args[0], "-test.run=^TestProcessGuardianAcceptanceChild$", "--", "host-"+mode+"-acceptance", directory)
			log, err := os.OpenFile(filepath.Join(directory, "host.log"), os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command.Stdout, command.Stderr = log, log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = command.Process.Kill()
				_ = os.WriteFile(filepath.Join(directory, "guardian-release"), nil, 0600)
			})
			processReleasePoll(t, func() bool { return processReleaseExists(filepath.Join(directory, "host-ready.json")) })
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			var exit *exec.ExitError
			if err := command.Wait(); !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("host did not die from actual SIGKILL: %v", err)
			}
			if !processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) {
				t.Fatal("guardian lost the original lease before processing host EOF")
			}
			if processReleaseExists(filepath.Join(directory, "native-started.json")) {
				t.Fatal("paused guardian admitted native before launch processing")
			}
			if err := os.WriteFile(filepath.Join(directory, "guardian-release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			processReleasePoll(t, func() bool { return !processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) })
			if processReleaseLockHeld(t, filepath.Join(directory, "native.lock")) {
				t.Fatal("host lease was released while native still held its actual resource")
			}
			admitted := processReleaseExists(filepath.Join(directory, "native-started.json"))
			if mode == "incomplete" && admitted {
				t.Fatal("incomplete launch JSON was incorrectly admitted")
			}
			guardianAcceptanceJSON(t, filepath.Join(directory, "result.json"), map[string]any{"HostPID": command.Process.Pid, "Signal": "SIGKILL", "Launch": mode, "Admitted": admitted, "LeaseBusyBeforeOwnerProcessing": true, "LeaseReleasedAfterOwnerCleanup": true, "NativeResourceReleased": true})
			t.Logf("host SIGKILL; launch=%s; native admitted=%v; original lease and actual resource released", mode, admitted)
		})
	}
}

// TestProcessGuardianAcceptanceTimeoutRetainsLease checks that a timed-out Close does not certify release.
func TestProcessGuardianAcceptanceTimeoutRetainsLease(t *testing.T) {
	directory := processReleaseDirectory(t)
	lease := guardianAcceptanceLease(t, directory)
	spec := guardianAcceptanceNativeSpec(directory)
	guardian, duplicated := guardianAcceptanceConfig(t, directory, lease)
	spec.Guardian = guardian
	owner, err := NewProcess(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan error, 1)
	go func() { started <- owner.Start(ctx) }()
	processReleasePoll(t, func() bool { return processReleaseExists(filepath.Join(directory, "guardian-ready.json")) })
	<-duplicated // Cross-process files establish the OS boundary; this channel also establishes Go happens-before.
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-started; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked Start did not report cancellation: %v", err)
	}
	closeCtx, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stop()
	if err := owner.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("paused owner did not report Close timeout: %v", err)
	}
	if !processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) {
		t.Fatal("Close timeout prematurely released the original kernel lease")
	}
	if err := os.WriteFile(filepath.Join(directory, "guardian-release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	closeCtx, finish := context.WithTimeout(t.Context(), 10*time.Second)
	defer finish()
	if err := owner.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) || processReleaseLockHeld(t, filepath.Join(directory, "native.lock")) {
		t.Fatal("successful Close did not release the original lease and native resources")
	}
	if err := owner.Start(t.Context()); !errors.Is(err, ErrProcessAlreadyUsed) {
		t.Fatalf("same cancelled owner admitted a second launch: %v", err)
	}
	guardianAcceptanceJSON(t, filepath.Join(directory, "result.json"), map[string]any{"StartCancelled": true, "CloseTimeout": true, "LeaseBusyAfterTimeout": true, "LeaseReleasedAfterSameOwnerCleanup": true, "SecondStartRejected": true})
}

// TestProcessGuardianAcceptanceNativeStartupFailureReleasesLease removes the frozen native executable before guardian admission.
func TestProcessGuardianAcceptanceNativeStartupFailureReleasesLease(t *testing.T) {
	directory := processReleaseDirectory(t)
	lease := guardianAcceptanceLease(t, directory)
	executable := filepath.Join(directory, "native-removed-before-admission")
	if err := os.Symlink(os.Args[0], executable); err != nil {
		t.Fatal(err)
	}
	spec := guardianAcceptanceNativeSpec(directory)
	spec.Executable = executable
	guardian, duplicated := guardianAcceptanceConfig(t, directory, lease)
	spec.Guardian = guardian
	owner, err := NewProcess(spec)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- owner.Start(t.Context()) }()
	processReleasePoll(t, func() bool { return processReleaseExists(filepath.Join(directory, "guardian-ready.json")) })
	<-duplicated
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) {
		t.Fatal("startup failure fixture lost the original guardian lease")
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "guardian-release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-started; err == nil {
		t.Fatal("missing native executable was reported as admitted")
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := owner.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) || processReleaseExists(filepath.Join(directory, "native-started.json")) {
		t.Fatal("failed native startup leaked ownership or admitted a native process")
	}
	guardianAcceptanceJSON(t, filepath.Join(directory, "result.json"), map[string]any{"FrozenExecutableRemoved": true, "StartRejected": true, "NativeAdmitted": false, "LeaseReleasedAfterCleanup": true})
}

// TestProcessGuardianAcceptanceInvalidOwnerCannotSpawn checks admission before allocating a guardian or native process.
func TestProcessGuardianAcceptanceInvalidOwnerCannotSpawn(t *testing.T) {
	for _, mode := range []string{"relative-executable", "nil-lease", "lease-error"} {
		t.Run(mode, func(t *testing.T) {
			directory := processReleaseDirectory(t)
			guardian := &ProcessGuardian{Executable: os.Args[0], DuplicateLease: func() (*os.File, error) { return nil, nil }}
			if mode == "relative-executable" {
				guardian.Executable = "relative-guardian"
			} else if mode == "lease-error" {
				guardian.DuplicateLease = func() (*os.File, error) { return nil, os.ErrClosed }
			}
			spec := guardianAcceptanceNativeSpec(directory)
			spec.Guardian = guardian
			owner, err := NewProcess(spec)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Start(t.Context()); err == nil {
				t.Fatal("invalid original owner was accepted")
			}
			if err := owner.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if owner.child != nil || processReleaseExists(filepath.Join(directory, "native-started.json")) {
				t.Fatal("invalid owner admitted a guardian or native process")
			}
		})
	}
}

// TestProcessGuardianAcceptanceNativeCannotInheritPrivateHandles checks actual fstat objects in the CLI.
func TestProcessGuardianAcceptanceNativeCannotInheritPrivateHandles(t *testing.T) {
	directory := processReleaseDirectory(t)
	lease := guardianAcceptanceLease(t, directory)
	spec := guardianAcceptanceNativeSpec(directory)
	guardian, duplicated := guardianAcceptanceConfig(t, directory, lease)
	spec.Guardian = guardian
	owner, err := NewProcess(spec)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- owner.Start(t.Context()) }()
	processReleasePoll(t, func() bool { return processReleaseExists(filepath.Join(directory, "guardian-ready.json")) })
	<-duplicated
	private := []guardianAcceptanceFD{}
	guardianAcceptanceRead(t, filepath.Join(directory, "guardian-ready.json"), &private)
	owner.mu.Lock()
	private = append(private, guardianAcceptanceIdentity(t, int(owner.remote.control.Fd()), "host-control-write"))
	owner.mu.Unlock()
	if err := os.WriteFile(filepath.Join(directory, "guardian-release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = owner.Close(ctx)
	})
	processReleasePoll(t, func() bool { return processReleaseExists(filepath.Join(directory, "native-started.json")) })
	var actual struct {
		// PID is the real native process that enumerated its descriptors.
		PID int
		// Descriptors are the actual inherited kernel objects, including permitted protocol streams.
		Descriptors []guardianAcceptanceFD
	}
	guardianAcceptanceRead(t, filepath.Join(directory, "native-started.json"), &actual)
	for _, forbidden := range private {
		for _, inherited := range actual.Descriptors {
			if forbidden.Device == inherited.Device && forbidden.Inode == inherited.Inode && forbidden.Mode == inherited.Mode {
				t.Fatalf("native PID %d inherited private %s through actual fd %s: %+v", actual.PID, forbidden.Name, inherited.Name, inherited)
			}
		}
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) {
		t.Fatal("guardian did not retain the same lease while native was live")
	}
	if err := owner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if processReleaseLockHeld(t, filepath.Join(directory, "core.lock")) {
		t.Fatal("a private lease handle leaked into native or its process tree")
	}
	guardianAcceptanceJSON(t, filepath.Join(directory, "result.json"), map[string]any{"NativePID": actual.PID, "PrivateHandles": private, "NativeDescriptors": actual.Descriptors, "PrivateHandleLeaks": 0, "LeaseReleased": true})
	t.Logf("actual native PID %d inherited %d descriptors, none matched %d private kernel objects", actual.PID, len(actual.Descriptors), len(private))
}

// TestProcessGroupReleaseAcceptanceRequiresEmptyObservation covers failure and nonempty observation contracts.
func TestProcessGroupReleaseAcceptanceRequiresEmptyObservation(t *testing.T) {
	calls := 0
	if err := waitProcessGroupExit(123, func(pid int) (bool, error) {
		if pid != 123 {
			t.Fatalf("observer received another owner's identity: %d", pid)
		}
		calls++
		return calls < 3, nil
	}); err != nil || calls != 3 {
		t.Fatalf("group release did not wait for an actually empty observation: calls=%d error=%v", calls, err)
	}
	failure := fmt.Errorf("kernel group observation denied")
	if err := waitProcessGroupExit(123, func(int) (bool, error) { return false, failure }); !errors.Is(err, failure) {
		t.Fatalf("unknown group state was mistaken for empty: %v", err)
	}
}
