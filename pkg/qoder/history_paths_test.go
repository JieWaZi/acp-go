package qoder

import (
	"context"
	"strings"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
)

func TestQoderJournalLongPathMatchesNativeEncoder(t *testing.T) {
	// Native Ir keeps all 200 slug characters before appending the hash.
	want := "-tmp-" + strings.Repeat("a", 195) + "-ch2fh0"
	if got := qoderProjectSlug("/tmp/" + strings.Repeat("a", 210)); got != want {
		t.Fatalf("native long-path encoding differs: length %d, want %d", len(got), len(want))
	}
}

func TestQoderJournalRejectsConflictingInitializedVersion(t *testing.T) {
	capture := captureQoderHistory(context.Background(), acpclient.PromptJournalScope{Version: "1.1.84", LauncherVersion: "1.1.83", Cwd: t.TempDir(), SessionID: acp.SessionId("11111111-1111-4111-8111-111111111111")}, t.TempDir())
	if capture != nil {
		t.Fatal("launcher version overrode unsupported initialized version")
	}
}

func TestQoderPersistedUserReceiptSurvivesCancellation(t *testing.T) {
	evidence := readPromptEvidence(acp.StopReasonCancelled, "22222222-2222-4222-8222-222222222222", nil)
	if !evidence.InputAccepted || evidence.NativeTurnID == "" {
		t.Fatal("native persisted input identity was discarded on cancellation")
	}
}
