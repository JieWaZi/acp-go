package userinput

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
)

// sessionOwnerAgent controls whether the wrapped adapter owns native session cancellation.
type sessionOwnerAgent struct {
	// Agent provides the untouched ACP protocol surface.
	acp.Agent
	// owns is the opt-in reported by the underlying adapter.
	owns bool
}

// ManagesSessionLifecycle returns the adapter's marker.
func (a sessionOwnerAgent) ManagesSessionLifecycle() bool { return a.owns }

// TestWrapPreservesSessionLifecycleOwnership tests the actual wrapper used by acpserver.
func TestWrapPreservesSessionLifecycleOwnership(t *testing.T) {
	tests := []struct {
		// agent is the adapter installed by the host.
		agent acp.Agent
		// owns is the ownership flag visible after wrapping.
		owns bool
	}{
		{nil, false},
		{sessionOwnerAgent{}, false},
		{sessionOwnerAgent{owns: true}, true},
	}
	for _, tt := range tests {
		var wrapped any = Wrap(tt.agent)
		owner, supported := wrapped.(interface {
			// ManagesSessionLifecycle reports the wrapped adapter's marker.
			ManagesSessionLifecycle() bool
		})
		if !supported || owner.ManagesSessionLifecycle() != tt.owns {
			t.Fatalf("wrapper of %T changed ownership: supported=%v, want owns=%v", tt.agent, supported, tt.owns)
		}
	}
}

// TestPromptReadinessEndsWhenEndpointStops verifies that a captured endpoint
// cannot keep the operation waiting for tools after that endpoint has stopped.
func TestPromptReadinessEndsWhenEndpointStops(t *testing.T) {
	wrapped := Wrap(nil)
	wrapped.requireReady = true
	ep, err := newEndpoint(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ep.stop)
	if err := wrapped.attach(ep, "session", nil); err != nil {
		t.Fatal(err)
	}
	ep.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = wrapped.Prompt(ctx, acp.PromptRequest{SessionId: "session"})
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopped endpoint should fail before the request deadline: %v", err)
	}
}

// TestPromptPreparationRejectsReplacedEndpoint proves that preparation cannot
// attach an admitted operation to another generation's user-input server.
func TestPromptPreparationRejectsReplacedEndpoint(t *testing.T) {
	wrapped := Wrap(nil)
	request := acp.PromptRequest{SessionId: "session"}
	newSessionEndpoint := func() *endpoint {
		t.Helper()
		ep, err := newEndpoint(nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(ep.stop)
		return ep
	}
	if err := wrapped.attach(newSessionEndpoint(), request.SessionId, nil); err != nil {
		t.Fatal(err)
	}
	captured := wrapped.capturePromptPreparation(request)
	finish, err := wrapped.beginEndpointChange(request.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	duringChange := wrapped.capturePromptPreparation(request)
	if _, err := wrapped.beginEndpointChange(request.SessionId); !errors.Is(err, errEndpointUnavailable) {
		t.Fatalf("overlapping endpoint mutation returned %v", err)
	}
	next := newSessionEndpoint()
	if err := wrapped.attach(next, request.SessionId, nil); err != nil {
		t.Fatal(err)
	}
	finish()

	for _, prepare := range []acp.PromptPreparation{captured, duringChange} {
		if _, err := prepare(context.Background()); !errors.Is(err, errEndpointUnavailable) {
			t.Fatalf("stale preparation returned %v", err)
		}
	}
	prepare := wrapped.capturePromptPreparation(request)
	cleanup, err := prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanup()

	nextCleanup, err := prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer nextCleanup()
	cleanup()
	if next.turn == nil || next.turn.Err() != nil {
		t.Fatal("previous cleanup cleared the current interaction")
	}
}

// admissionProbe installs a single-operation owner for the test.
type admissionProbe struct {
	// Agent supplies protocol methods the test does not call.
	acp.Agent
	// install binds the admission hooks when the connection is assigned.
	install acp.SessionAdmission
}

func (a admissionProbe) SetAgentConnection(connection *acp.AgentSideConnection) {
	connection.SetSessionAdmission(a.install)
}

func (admissionProbe) Cancel(context.Context, acp.CancelNotification) error {
	return errors.New("unexpected legacy cancel")
}

// TestBoundPromptCancelsCapturedReadiness verifies the direct wrapper path uses
// the installed owner and gives readiness the same cancellation scope.
func TestBoundPromptCancelsCapturedReadiness(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	entered := make(chan context.CancelFunc, 1)
	var stopOwner context.CancelFunc
	native := admissionProbe{install: acp.SessionAdmission{
		Prompt: func(ctx context.Context, _ acp.PromptRequest, prepare acp.PromptPreparation) (func() (acp.PromptResponse, error), error) {
			ownerCtx, cancel := context.WithCancel(ctx)
			return func() (acp.PromptResponse, error) {
				defer cancel()
				entered <- cancel
				if _, err := prepare(ownerCtx); !errors.Is(err, context.Canceled) {
					return acp.PromptResponse{}, errors.New("readiness did not observe cancellation")
				}
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}, nil
		},
		Cancel: func(acp.CancelNotification) (func(context.Context) error, error) {
			stopOwner()
			return nil, nil
		},
	}}
	wrapped := Wrap(native)
	wrapped.requireReady = true
	input, peer := io.Pipe()
	defer peer.Close()
	acp.NewAgentSideConnectionWithBinder(wrapped, io.Discard, input, wrapped.SetAgentConnection)
	ep, err := newEndpoint(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ep.stop)
	if err := wrapped.attach(ep, "session", nil); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		response, err := wrapped.Prompt(ctx, acp.PromptRequest{SessionId: "session"})
		if err == nil && response.StopReason != acp.StopReasonCancelled {
			err = errors.New("owner cancellation result was lost")
		}
		done <- err
	}()
	select {
	case stopOwner = <-entered:
	case <-ctx.Done():
		t.Fatal("direct Prompt did not admit before waiting for readiness")
	}
	if err := wrapped.Cancel(ctx, acp.CancelNotification{SessionId: "session"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || ctx.Err() != nil {
			t.Fatalf("direct Prompt failed while cancelling readiness: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("cancel did not unblock readiness")
	}
}
