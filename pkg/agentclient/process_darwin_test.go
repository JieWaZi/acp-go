package agentclient

import (
	"bufio"
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProcessDarwinRetainsLiveGroupPermissionFailure(t *testing.T) {
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, processFixture(t, "graceful"))
	if _, err := bufio.NewReader(p.Stdout()).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := groupSignalError(p.child.Pid, unix.EPERM); !errors.Is(err, unix.EPERM) {
		t.Fatalf("a live leader's permission failure was hidden: %v", err)
	}
}
