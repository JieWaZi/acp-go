package acp

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestToReqErr_ContextCanceledMapsToRequestCancelled(t *testing.T) {
	wrapped := errors.Join(context.Canceled, errors.New("extra context"))
	re := toReqErr(wrapped)
	if re == nil {
		t.Fatal("expected request error")
	}
	if re.Code != -32800 {
		t.Fatalf("expected code -32800, got %d", re.Code)
	}
}

func TestToReqErr_DeadlineExceededMapsToInternalError(t *testing.T) {
	re := toReqErr(context.DeadlineExceeded)
	if re == nil {
		t.Fatal("expected request error")
	}
	if re.Code != -32603 {
		t.Fatalf("expected code -32603, got %d", re.Code)
	}
}

// TestToReqErrPreservesWrappedNativeFacts protects error data at the JSON-RPC boundary.
func TestToReqErrPreservesWrappedNativeFacts(t *testing.T) {
	failure := &RequestError{Code: -32098, Message: "native failure", Data: map[string]any{"submission": "accepted"}}
	got := toReqErr(fmt.Errorf("wrapping a technical failure: %w", failure))
	if got != failure {
		t.Fatalf("wrapped request error became %v", got)
	}
	plain := toReqErr(errors.New("ordinary Go error"))
	if plain.Code != -32603 || plain.Message != "Internal error" {
		t.Fatalf("plain error changed mapping: %v", plain)
	}
}
