//go:build darwin || linux

package agentclient

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProcessGuardianEntry(t *testing.T) {
	if os.Args[len(os.Args)-1] == "guardian-entry" {
		os.Exit(RunProcessGuardian())
	}
}

func guardianTestLease(t *testing.T) (*os.File, *ProcessGuardian, func() bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owner.lock")
	lease, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	guardian := &ProcessGuardian{Executable: os.Args[0], Args: []string{"-test.run=^TestProcessGuardianEntry$", "--", "guardian-entry"}, DuplicateLease: func() (*os.File, error) {
		fd, err := unix.FcntlInt(lease.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "test-owner-lease"), nil
	}}
	busy := func() bool {
		t.Helper()
		probe, err := os.OpenFile(path, os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer probe.Close()
		err = unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		return err != nil
	}
	return lease, guardian, busy
}

func TestProcessGuardianOwnsOriginalTreeAndLease(t *testing.T) {
	lease, guardian, busy := guardianTestLease(t)
	spec := processFixture(t, "running-tree")
	spec.Guardian = guardian
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, spec)
	if _, err := bufio.NewReader(p.Stdout()).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !busy() {
		t.Fatal("guardian did not own the host lease before native dispatch")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if busy() {
		t.Fatal("completed guardian retained the old host lease")
	}
}

func TestProcessGuardianPreservesOriginalExitAndOutput(t *testing.T) {
	_, guardian, _ := guardianTestLease(t)
	spec := processFixture(t, "exit")
	spec.Guardian = guardian
	spec.Redact = func(record string) string { return record }
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, spec)
	exit, err := p.Wait(ctx)
	if err != nil || exit.Code != 19 || exit.Err == nil || exit.Diagnostic != "fixture failure" {
		t.Fatalf("native outcome changed: %+v %v", exit, err)
	}
	output, err := io.ReadAll(p.Stdout())
	if err != nil || string(output) != "{\"final\":true}\n" {
		t.Fatalf("native output changed: %q %v", output, err)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
