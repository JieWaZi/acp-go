package acpmeta

import (
	"errors"
	acp "github.com/coder/acp-go-sdk"
	"os"
	"path/filepath"
	"testing"
)

// TestForkReceiptIsolationAndReconciliation 验证原生分叉身份、边界及隔离约束。
func TestForkReceiptIsolationAndReconciliation(t *testing.T) {
	parent := t.TempDir()
	request := acp.UnstableForkSessionRequest{SessionId: "native-parent", Cwd: filepath.Join(parent, "workspace"), Meta: PositionMetadata("turn-before")}
	request.Meta["forkReceiptDirectory"] = filepath.Join(parent, "receipt-1")
	if _, err := ReadForkReceipt(request); !errors.Is(err, ErrForkUnconfirmed) {
		t.Fatal(err)
	}
	if err := BeginForkReceipt(request); err != nil {
		t.Fatal(err)
	}
	if err := BeginForkReceipt(request); !errors.Is(err, ErrForkUnconfirmed) {
		t.Fatalf("reused operation directory must not fork again: %v", err)
	}
	if err := WriteForkReceipt(request, "native-child"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(request.Cwd); !os.IsNotExist(err) {
		t.Fatal("receipt leaked into workspace")
	}
	if got, err := ReadForkReceipt(request); err != nil || got != "native-child" {
		t.Fatalf("%s %v", got, err)
	}
	request.SessionId = "other-profile"
	if _, err := ReadForkReceipt(request); !errors.Is(err, ErrForkUnconfirmed) {
		t.Fatal("cross-profile receipt reused")
	}
	if err := WriteForkReceipt(request, request.SessionId); !errors.Is(err, ErrForkUnconfirmed) {
		t.Fatal("parent reused")
	}
	request.SessionId = "native-parent"
	if err := WriteForkReceipt(request, "another-child"); !errors.Is(err, ErrForkUnconfirmed) {
		t.Fatal("existing receipt changed identity")
	}
	invalid := acp.UnstableForkSessionRequest{SessionId: request.SessionId, Cwd: "/tmp"}
	if err := BeginForkReceipt(invalid); err == nil {
		t.Fatal("missing operation directory accepted")
	}
}

// TestVerifiedForkVersion 验证原生分叉身份、边界及隔离约束。
func TestVerifiedForkVersion(t *testing.T) {
	for _, version := range []string{"", "garbled", "0.149.1"} {
		if VerifiedForkMode(version, "0.155.1", ForkTurn) != ForkUnsupported {
			t.Fatal(version)
		}
	}
	if VerifiedForkMode("codex-cli 0.155.1", "0.155.1", ForkTurn) != ForkTurn {
		t.Fatal("verified Codex disabled")
	}
	if VerifiedForkMode("2026.09.10-fd3934a", "2026.09.10", ForkLatest) != ForkLatest {
		t.Fatal("verified Cursor disabled")
	}
	if VerifiedForkModeInMinor("2026.09.18-9a7762b", "2026.09.10", ForkLatest) != ForkLatest ||
		VerifiedForkModeInMinor("2026.10.01", "2026.09.10", ForkLatest) != ForkUnsupported ||
		VerifiedForkModeInMinor("2.1.158", "2.1.159", ForkLatest) != ForkUnsupported {
		t.Fatal("private fork format version boundary is incorrect")
	}
}
