package claude

import (
	"context"
	"errors"
	"slices"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
)

// nativeOpening records the first evidence of a main-thread execution.
// Liveness can nominate a host input, but only an opening reply can prove it.
type nativeOpening struct {
	candidate *input
	initiator *input
	observed  bool
	recovery  string
}

// Claude 2.1.159 has no native turn input IDs. Its stream-json replay
// acknowledges the exact host UUID instead, without identifying a queue slot.
func (c *Client) usesLegacyReplay() bool {
	return c.version == "2.1.159"
}

func (s *session) observeLegacyEchoLocked(message *protocol.UserMessage) {
	if !s.client.usesLegacyReplay() || !message.IsReplay || message.SessionID != string(s.id) ||
		message.ParentToolUseID != nil || message.Message.Role != "user" || len(s.sent) != 1 ||
		s.nativeQueued > 0 || s.background > 0 {
		return
	}
	if origin := message.Origin; origin != nil && origin.Kind != "" && origin.Kind != "human" {
		return
	}
	input := s.sent[message.UUID]
	if input == nil || input.mayFollowProvider || input.message.Priority != "" {
		return
	}
	inv := s.current
	if inv != nil && inv.opening.candidate != input {
		return
	}
	input.legacyEchoed = true
	if inv != nil && inv.opening.observed {
		s.confirmLegacyOpeningLocked(inv)
	}
}

// A competing native task invalidates the provisional opening even if it
// finishes before the replay arrives. A confirmed host edge is independent.
func (s *session) fenceLegacyOpeningLocked() {
	if !s.client.usesLegacyReplay() || len(s.sent) == 0 {
		return
	}
	for _, input := range s.sent {
		input.mayFollowProvider = true
	}
	if s.current != nil {
		s.current.opening.candidate = nil
	}
}

func (s *session) confirmLegacyOpeningLocked(inv *invocation) {
	input := inv.opening.candidate
	if input == nil || !input.legacyEchoed || s.sent[input.identity.InputID] != input ||
		s.nativeQueued > 0 || s.background > 0 {
		return
	}
	inv.opening.candidate = nil
	inv.opening.initiator = input
	inv.outcome.Turn.Origin = acpmeta.NativeTurnOriginHost
	inv.scope.HostInvocationID = input.identity.HostInvocationID
	s.recordConsumedIDsLocked(inv, []string{input.identity.InputID})
}

func consumedIDs(identity protocol.TurnIdentity) []string {
	ids := identity.UserMessageUUIDs
	if len(ids) == 0 && identity.UserMessageUUID != "" {
		ids = []string{identity.UserMessageUUID}
	}
	out := make([]string, 0, min(len(ids), maxPendingInputs))
	for _, id := range ids {
		if !isUUID(id) || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
		if len(out) == maxPendingInputs {
			break
		}
	}
	return out
}

func (s *session) observeLivenessLocked() *invocation {
	if s.current != nil {
		return s.current
	}
	return s.beginNativeLocked()
}

func (s *session) beginNativeLocked() *invocation {
	if !s.ledger.reserve() {
		s.faultAndStopLocked(errCapacity)
		return nil
	}
	id, err := newUUID()
	if err != nil {
		s.ledger.complete(agentclient.NativeOutcome{}, false)
		s.faultAndStopLocked(err)
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	inv := &invocation{outcome: agentclient.NativeOutcome{
		Turn: acpmeta.NativeTurn{InvocationID: id, Origin: acpmeta.NativeTurnOriginUnknown}, PossibleSideEffects: true,
	}, scope: agentclient.Scope{SessionID: s.id, NativeInvocationID: id}, seenInputs: make(map[string]struct{}),
		ctx: ctx, cancel: cancel, closed: make(chan struct{}), partialUsage: make(map[string]requestUsage),
	}
	if len(s.sent) == 1 && s.nativeQueued == 0 {
		for _, in := range s.sent {
			if !in.mayFollowProvider && in.message.Priority == "" {
				inv.opening.candidate = in
			}
		}
	}
	s.current = inv
	s.epoch++
	s.signalLocked()
	return inv
}

func (s *session) observeMainLocked(identity protocol.TurnIdentity) *invocation {
	inv := s.current
	if inv != nil && inv.opening.observed && identity.ResumeReason != "" && inv.opening.recovery != identity.ResumeReason {
		inv.outcome.OperationError = errors.New("Claude recovered before the prior native result was observed")
		s.sealLocked(inv)
		s.counters.breakContinuity()
		inv = nil
	}
	if inv == nil {
		inv = s.beginNativeLocked()
	}
	if inv != nil {
		s.observeOpeningLocked(inv, identity, false)
		s.recordConsumptionLocked(inv, identity)
	}
	return inv
}

func (s *session) observeOpeningLocked(inv *invocation, identity protocol.TurnIdentity, anonymousGap bool) {
	if inv.opening.observed {
		return
	}
	inv.opening.observed = true
	inv.opening.recovery = identity.ResumeReason
	provider := identity.ResumeReason != "" || identity.Origin != nil && identity.Origin.Kind != "" && identity.Origin.Kind != "human"
	ids := consumedIDs(identity)
	candidate := inv.opening.candidate
	switch {
	case provider:
		inv.outcome.Turn.Origin = acpmeta.NativeTurnOriginProvider
	case candidate != nil && len(ids) > 0 && ids[0] == candidate.identity.InputID &&
		s.sent[ids[0]] == candidate:
		inv.opening.initiator = candidate
		inv.outcome.Turn.Origin = acpmeta.NativeTurnOriginHost
		inv.scope.HostInvocationID = candidate.identity.HostInvocationID
	case s.client.usesLegacyReplay() && !anonymousGap && candidate != nil && len(ids) == 0:
		// The first main frame can precede the stdin acknowledgement.
		s.confirmLegacyOpeningLocked(inv)
		return
	case !anonymousGap && len(s.sent) == 0 && len(ids) == 0:
		inv.outcome.Turn.Origin = acpmeta.NativeTurnOriginProvider
	}
	inv.opening.candidate = nil
}

func (s *session) recordConsumptionLocked(inv *invocation, identity protocol.TurnIdentity) {
	s.recordConsumedIDsLocked(inv, consumedIDs(identity))
}

func (s *session) recordConsumedIDsLocked(inv *invocation, ids []string) {
	for _, id := range ids {
		if len(inv.seenInputs) >= maxPendingInputs {
			break
		}
		if _, seen := inv.seenInputs[id]; seen {
			continue
		}
		// A UUID is an input edge, not an invocation identity. Recovery may
		// legitimately name a previous invocation's consumed UUID again.
		if _, withdrawn := s.withdrawnInputs[id]; withdrawn {
			continue
		}
		inv.seenInputs[id] = struct{}{}
		inv.outcome.Turn.ConsumedInputIDs = append(inv.outcome.Turn.ConsumedInputIDs, id)
		if in := s.sent[id]; in != nil {
			delete(s.sent, id)
			in.inv = inv
			in.phase = consumed
			in.submission = agentclient.SubmissionSent
			in.admission = agentclient.AdmissionConfirmed
			inv.inputs = append(inv.inputs, in)
			inv.compact = inv.compact || in.compact
			s.reconcileCancellationLocked(in)
		}
	}
	s.signalLocked()
}

// associateResultLocked uses the process result sequence and opening evidence
// to select the native execution. It never rejects a result because its input
// waiter already returned or the same input appeared in a previous result.
func (s *session) associateResultLocked(identity protocol.TurnIdentity, gap bool) (current, lost *invocation) {
	current = s.current
	ids := consumedIDs(identity)
	if current != nil && current.opening.observed {
		related := slices.ContainsFunc(ids, func(id string) bool {
			_, seen := current.seenInputs[id]
			return seen
		})
		recoveryChanged := identity.ResumeReason != "" && identity.ResumeReason != current.opening.recovery
		foreignInput := len(ids) > 0 && !related && current.opening.initiator != nil &&
			!slices.ContainsFunc(ids, func(id string) bool { return s.sent[id] != nil })
		if recoveryChanged || gap && !related || foreignInput {
			current.outcome.OperationError = errors.New("Claude did not report the preceding native result")
			s.freezeNativeLocked(current)
			lost, current = current, nil
			s.counters.breakContinuity()
		}
	} else if current != nil && gap {
		// A status-only owner has no opening identity to bridge a gap.
		current.outcome.OperationError = errors.New("Claude native result sequence gap")
		s.freezeNativeLocked(current)
		lost, current = current, nil
	}
	if gap {
		s.counters.breakContinuity()
	}
	if current == nil {
		current = s.beginNativeLocked()
	}
	if current != nil {
		s.observeOpeningLocked(current, identity, gap)
		s.recordConsumptionLocked(current, identity)
	}
	return current, lost
}

// observeCompactionLocked records manual command evidence at the boundary.
// Generic status and automatic boundaries have no manual input provenance.
func (s *session) observeCompactionLocked(m *protocol.SystemMessage) {
	if m.Subtype != "compact_boundary" || m.CompactMetadata == nil || m.CompactMetadata.Trigger != "manual" {
		return
	}
	inv := s.observeLivenessLocked()
	if inv == nil {
		return
	}
	input := inv.opening.initiator
	if !inv.opening.observed {
		input = inv.opening.candidate
	}
	if input != nil && input.compact {
		inv.manualCompact = input
	}
}
