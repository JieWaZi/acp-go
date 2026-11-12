package acpclient

import (
	"encoding/json"
	"errors"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
)

func TestContinuationFoldsConfigurationWithoutRetainingHistory(t *testing.T) {
	p := &opening{session: &session{id: "target"}, started: true, replay: true, continuation: true}
	c := &Client{opening: p, opts: Options{Protocol: Protocol{RequireLoadEvidence: true}}}
	for i := range maxOpeningFrames + 1 {
		c.receive(acp.ReceiveNotification{
			Method: acp.ClientMethodSessionUpdate, Sequence: uint64(i + 1),
			Params: json.RawMessage(`{"sessionId":"target","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old answer"}}}`),
		})
	}
	c.receive(acp.ReceiveNotification{
		Method: acp.ClientMethodSessionUpdate, Sequence: maxOpeningFrames + 2,
		Params: json.RawMessage(`{"sessionId":"target","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"restored","options":[{"value":"restored","name":"Restored"}]}]}}`),
	})
	c.bindOpening(p, acp.RequestReceipt[setupResult]{ResultValid: true, Sequence: maxOpeningFrames + 3})
	if !p.matched || p.err != nil || p.overflow || len(p.observations) != 0 || p.observationBytes != 0 {
		t.Fatalf("restore did not settle without replay: matched=%v error=%v overflow=%v observations=%d bytes=%d",
			p.matched, p.err, p.overflow, len(p.observations), p.observationBytes)
	}
	if model := modelOption(p.session.config.options); model == nil || model.CurrentValue != "restored" {
		t.Fatalf("restore lost configuration observed before the response: %+v", model)
	}
}

func TestContinuationRequiresEvidenceForTheRequestedSession(t *testing.T) {
	p := &opening{session: &session{id: "target"}, started: true, replay: true, continuation: true}
	c := &Client{opening: p, opts: Options{Protocol: Protocol{RequireLoadEvidence: true}}}
	c.receive(acp.ReceiveNotification{
		Method: acp.ClientMethodSessionUpdate, Sequence: 1,
		Params: json.RawMessage(`{"sessionId":"other","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"other history"}}}`),
	})
	c.bindOpening(p, acp.RequestReceipt[setupResult]{ResultValid: true, Sequence: 2})
	var rejection *acp.RequestError
	if p.matched || !errors.As(p.err, &rejection) || rejection.Message != "ACP agent did not verify restored history" {
		t.Fatalf("unverified restore: matched=%v error=%v", p.matched, p.err)
	}
}
