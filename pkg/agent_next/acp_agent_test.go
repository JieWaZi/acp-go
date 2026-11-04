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
