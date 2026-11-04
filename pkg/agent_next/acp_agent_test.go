package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type wireResultClient struct {
	Client
	result PromptResult
}

type wireIngressClient struct {
	Client
	reserved  chan struct{}
	cancelled chan struct{}
	held      chan struct{}
	release   chan struct{}
}

func (c *wireIngressClient) ReservePrompt(context.Context, acp.PromptRequest, PromptOptions) (func() PromptResult, error) {
	close(c.reserved)
	return func() PromptResult {
		<-c.cancelled
		return PromptResult{Receipt: PromptReceipt{Submission: SubmissionNotSent, Admission: AdmissionNotAccepted, OperationError: context.Canceled}}
	}, nil
}

func (c *wireIngressClient) Cancel(acp.CancelNotification) error {
	select {
	case <-c.reserved:
	default:
		return errors.New("cancel arrived before reservation")
	}
	close(c.cancelled)
	return nil
}

func (c *wireIngressClient) HandleExtensionMethod(_ context.Context, method string, _ json.RawMessage) (any, error) {
	if method != "_hold" {
		return nil, acp.NewMethodNotFound(method)
	}
	close(c.held)
	<-c.release
	return nil, nil
}

// The public wire must reserve a prompt before its neighboring cancel and handle
// that cancel independently of an already blocked notification dispatcher.
func TestACPProjectionCancelBypassesBlockedNotification(t *testing.T) {
	client := &wireIngressClient{reserved: make(chan struct{}), cancelled: make(chan struct{}), held: make(chan struct{}), release: make(chan struct{})}
	agent, err := NewACPAgent(func(HostCallbacks) (Client, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	t.Cleanup(func() {
		close(client.release)
		toAgent.Close()
		fromClient.Close()
		toClient.Close()
		fromAgent.Close()
	})
	acp.NewAgentSideConnectionWithBinder(agent, fromAgent, toAgent, agent.SetAgentConnection)
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(fromClient, "{\"jsonrpc\":\"2.0\",\"method\":\"_hold\"}\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/prompt\",\"params\":{\"sessionId\":\"session\",\"prompt\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n{\"jsonrpc\":\"2.0\",\"method\":\"session/cancel\",\"params\":{\"sessionId\":\"session\"}}\n")
		writeDone <- err
	}()
	for _, signal := range []<-chan struct{}{client.held, client.cancelled} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatal("cancel waited for the blocked notification dispatcher")
		}
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestACPProjectionDoesNotInventTerminalAfterLocalCancellation(t *testing.T) {
	response, err := projectPromptResult(PromptResult{
		Receipt: PromptReceipt{Submission: SubmissionSent, Admission: AdmissionUnknown, OperationError: context.Canceled},
		Outcome: &NativeOutcome{Turn: acpmeta.NativeTurn{InvocationID: "native", Origin: acpmeta.NativeTurnOriginHost}},
	})
	var wireErr *acp.RequestError
	if !errors.As(err, &wireErr) || wireErr.Code != -32800 || response.StopReason != "" {
		t.Fatalf("local cancellation projected as native terminal: %#v, %v", response, err)
	}
	turn, ok := acpmeta.ReadNativeTurn(wireErr.Data.(map[string]any))
	if !ok || turn.Status != "" {
		t.Fatalf("local cancellation invented status: %#v", turn)
	}
}

func (c *wireResultClient) ReservePrompt(context.Context, acp.PromptRequest, PromptOptions) (func() PromptResult, error) {
	return func() PromptResult { return c.result.Snapshot() }, nil
}

func (c *wireResultClient) Cancel(acp.CancelNotification) error { return nil }

type wireUnusedCallbacks struct{ acp.Client }

// JSON-RPC discards result when error is present. Known native facts must remain
// available to the wire consumer even though observation failed after completion.
func TestACPProjectionPreservesKnownOutcomeInError(t *testing.T) {
	stop := acp.StopReasonEndTurn
	input := int64(17)
	client := &wireResultClient{result: PromptResult{
		Receipt: PromptReceipt{Input: PromptSubmission{SessionID: "session", InputID: "input", HostInvocationID: "host"}, Submission: SubmissionSent, Admission: AdmissionConfirmed},
		Outcome: &NativeOutcome{Turn: acpmeta.NativeTurn{InvocationID: "native", Origin: acpmeta.NativeTurnOriginHost, Status: acpmeta.NativeTurnCompleted, Usage: &acpmeta.TurnUsage{InputTokens: &input, Source: "native"}}, StopReason: &stop, DeliveryError: errors.New("private observer diagnostic")},
	}}
	agent, err := NewACPAgent(func(HostCallbacks) (Client, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	t.Cleanup(func() { toAgent.Close(); fromClient.Close(); toClient.Close(); fromAgent.Close() })
	acp.NewAgentSideConnectionWithBinder(agent, fromAgent, toAgent, agent.SetAgentConnection)
	peer := acp.NewClientSideConnection(wireUnusedCallbacks{}, fromClient, toClient)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = peer.Prompt(ctx, acp.PromptRequest{SessionId: "session", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
	var wireErr *acp.RequestError
	if !errors.As(err, &wireErr) {
		t.Fatalf("expected wire error, got %v", err)
	}
	meta, ok := wireErr.Data.(map[string]any)
	if !ok {
		t.Fatalf("missing error evidence: %#v", wireErr.Data)
	}
	turn, ok := acpmeta.ReadNativeTurn(meta)
	if !ok || turn.InvocationID != "native" || turn.Status != acpmeta.NativeTurnCompleted || turn.Usage == nil || turn.Usage.InputTokens == nil || *turn.Usage.InputTokens != 17 {
		t.Fatalf("known terminal or usage lost across ACP: %#v", meta)
	}
	if meta["acp-go/delivery-error"] != true {
		t.Fatalf("delivery failure missing: %#v", meta)
	}
	if wireErr.Message == "private observer diagnostic" {
		t.Fatal("unclassified diagnostic escaped wire boundary")
	}
}

func TestACPProjectionKeepsNativePositionAndRiskEvidence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			stop := acp.StopReasonEndTurn
			outcome := &NativeOutcome{
				Turn:       acpmeta.NativeTurn{InvocationID: "native", Origin: acpmeta.NativeTurnOriginHost, Status: acpmeta.NativeTurnCompleted},
				StopReason: &stop, ForkPosition: "native-position",
				ObservedOutput: true, PossibleSideEffects: true,
			}
			input, output := int64(7), int64(4)
			outcome.Turn.Usage = &acpmeta.TurnUsage{
				Source: "native_main_loop", Coverage: "main_loop", InputTokens: &input, OutputTokens: &output,
				Observations: []acpmeta.UsageObservation{{Source: "cumulative_delta", Coverage: "query_pipeline", Tokens: acpmeta.ModelUsage{InputTokens: &input}}},
			}
			if failed {
				outcome.DeliveryError = acp.NewInternalError(map[string]any{"observedOutput": false, "possibleSideEffects": false})
			}
			peer := newProjectionPeer(t, &wireResultClient{result: PromptResult{
				Receipt: PromptReceipt{Submission: SubmissionSent, Admission: AdmissionConfirmed}, Outcome: outcome,
			}})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			response, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: "session", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
			meta := response.Meta
			if failed {
				var classified *acp.RequestError
				if !errors.As(err, &classified) {
					t.Fatalf("missing wire failure: %v", err)
				}
				meta, _ = classified.Data.(map[string]any)
			} else if err != nil {
				t.Fatal(err)
			}
			if acpmeta.ForkPosition(meta) != "native-position" || meta["observedOutput"] != true || meta["possibleSideEffects"] != true {
				t.Fatalf("canonical native evidence was lost or overwritten by delivery classification: %#v", meta)
			}
			usage, ok := acpmeta.ReadTurnUsage(meta)
			if !ok || usage.InputTokens == nil || *usage.InputTokens != 7 || usage.OutputTokens == nil || *usage.OutputTokens != 4 || len(usage.Observations) != 1 || usage.Observations[0].Coverage != "query_pipeline" || usage.Observations[0].Tokens.OutputTokens != nil {
				t.Fatalf("wire changed accounting or dropped overlapping evidence: %+v", usage)
			}
		})
	}
}

func newProjectionPeer(t *testing.T, client Client) *acp.ClientSideConnection {
	t.Helper()
	agent, err := NewACPAgent(func(HostCallbacks) (Client, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	t.Cleanup(func() { toAgent.Close(); fromClient.Close(); toClient.Close(); fromAgent.Close() })
	acp.NewAgentSideConnectionWithBinder(agent, fromAgent, toAgent, agent.SetAgentConnection)
	return acp.NewClientSideConnection(wireUnusedCallbacks{}, fromClient, toClient)
}

type wireSteeringOwner struct{ Client }

func (wireSteeringOwner) Steer(_ context.Context, request SteerRequest) (SteerResponse, error) {
	if request.PromptRequired {
		return SteerResponse{Outcome: "promptRequired", Reason: "noRunningTurn"}, nil
	}
	return SteerResponse{Outcome: "startedNewTurn"}, nil
}

func TestACPProjectionLegacySteeringIdleGate(t *testing.T) {
	peer := newProjectionPeer(t, wireSteeringOwner{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wire, err := peer.CallExtension(ctx, "_session/steering", json.RawMessage(`{"sessionId":"s","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":"promptRequired"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var response SteerResponse
	if err := json.Unmarshal(wire, &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome != "promptRequired" || response.Reason != "noRunningTurn" {
		t.Fatalf("wire idle policy was lost: %+v", response)
	}
}

func TestACPProjectionKnownNativeStopReasonInFault(t *testing.T) {
	nativeInputID := "d6a6bead-2bdd-4fc5-bf42-0cd21fb70650"
	for _, reason := range []acp.StopReason{acp.StopReasonEndTurn, acp.StopReasonMaxTokens} {
		t.Run(string(reason), func(t *testing.T) {
			peer := newProjectionPeer(t, &wireResultClient{result: PromptResult{
				Receipt: PromptReceipt{Input: PromptSubmission{NativeInputID: &nativeInputID}, Submission: SubmissionSent, Admission: AdmissionConfirmed},
				Outcome: &NativeOutcome{
					Turn:       acpmeta.NativeTurn{InvocationID: "native", Origin: acpmeta.NativeTurnOriginHost, Status: acpmeta.NativeTurnCompleted},
					StopReason: &reason, DeliveryError: errors.New("synthetic observer fault"),
				},
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: "s", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
			var wireErr *acp.RequestError
			if !errors.As(err, &wireErr) {
				t.Fatalf("expected wire error: %v", err)
			}
			data := wireErr.Data.(map[string]any)
			if data["nativeStopReason"] != string(reason) {
				t.Fatalf("wire error lost %q stop reason: %v", reason, data)
			}
			if data["nativeUserMessageId"] != nativeInputID {
				t.Fatalf("known acknowledgement lost: %#v", data)
			}
		})
	}
}

func TestACPProjectionRejectsInvalidSteeringBeforeDispatch(t *testing.T) {
	for _, params := range []string{
		`{"sessionId":"s","prompt":[],"_meta":{"steering":{"idleBehavior":"promptRequired"}}}`,
		`{"sessionId":"s","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":"invented"}}}`,
		`{"sessionId":"s","prompt":[{"type":"text","text":"hello"}],"_meta":{"steering":{"idleBehavior":17}}}`,
	} {
		peer := newProjectionPeer(t, wireSteeringOwner{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := peer.CallExtension(ctx, "_session/steering", json.RawMessage(params))
		cancel()
		var fault *acp.RequestError
		if !errors.As(err, &fault) || fault.Code != -32602 {
			t.Fatalf("invalid policy dispatched: %v", err)
		}
	}
}

type wireReserveFailureOwner struct{ Client }

func (wireReserveFailureOwner) ReservePrompt(context.Context, acp.PromptRequest, PromptOptions) (func() PromptResult, error) {
	return nil, errors.New("synthetic-private-reserve-detail")
}

func TestACPProjectionReserveFaultPrivacy(t *testing.T) {
	peer := newProjectionPeer(t, wireReserveFailureOwner{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := peer.Prompt(ctx, acp.PromptRequest{SessionId: "s", Prompt: []acp.ContentBlock{acp.TextBlock("hello")}})
	var wireErr *acp.RequestError
	if !errors.As(err, &wireErr) {
		t.Fatalf("expected wire error: %v", err)
	}
	if data, ok := wireErr.Data.(map[string]any); ok && data["error"] == "synthetic-private-reserve-detail" {
		t.Fatal("unclassified reservation failure escaped wire boundary")
	}
}

type wireReceiptOwner struct{ Client }

func (wireReceiptOwner) ReadNativeOutcomes(context.Context, acpmeta.NativeTurnReceiptsRequest, bool) (NativeOutcomePage, error) {
	return NativeOutcomePage{Outcomes: []NativeOutcome{{Turn: acpmeta.NativeTurn{InvocationID: "invocation", Origin: acpmeta.NativeTurnOriginProvider, Status: acpmeta.NativeTurnFailed}, NativeFailure: &acp.RequestError{Code: -32000, Message: "Authentication required", Data: map[string]any{"errorKind": "authentication_required", "phase": "prompt", "admission": "confirmed"}}, ObservedOutput: true}}}, nil
}

func TestACPProjectionNativeReceiptPreservesFailureClassification(t *testing.T) {
	peer := newProjectionPeer(t, wireReceiptOwner{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	data, err := peer.CallExtension(ctx, acpmeta.NativeTurnReceiptsMethod, acpmeta.NativeTurnReceiptsRequest{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	var page acpmeta.NativeTurnReceiptsResponse
	if err := json.Unmarshal(data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Receipts) != 1 || page.Receipts[0].Failure == nil {
		t.Fatal("missing native fault evidence")
	}
	failure := page.Receipts[0].Failure
	if failure.Code != -32000 || failure.Kind != "authentication_required" || failure.Phase != "prompt" || failure.Admission != "confirmed" {
		t.Fatalf("classified native receipt failure degraded: %+v", failure)
	}
}
