package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/agent_next/claude/protocol"
)

// assistantOutput tracks native request IDs, per-block stream deltas, and
// complete assistant wrappers separately. A request can finish many blocks
// under the same message.id. A tool boundary retires its preceding narration.
type assistantOutput struct {
	request        string
	segment        uint64
	textID         string
	thoughtID      string
	blocks         map[int]*assistantStreamBlock
	frames         map[string]struct{}
	toolBoundaries map[string]struct{}
	answer         []assistantTextBlock
	answerBytes    int
	streamBytes    int
	limited        bool
	rendered       bool
}

type assistantStreamBlock struct {
	segment  uint64
	id       string
	thinking bool
	prefix   string
	clipped  bool
}

type assistantTextBlock struct {
	frame string
	text  string
}

func (o *assistantOutput) beginRequest(nativeID string) {
	if nativeID == "" || o.request == nativeID {
		return
	}
	if o.request != "" {
		o.nextSegment()
		o.blocks = nil
		o.streamBytes = 0
	}
	o.request = nativeID
}

func (o *assistantOutput) nextSegment() {
	o.segment++
	o.textID = ""
	o.thoughtID = ""
	o.answer = nil
	o.answerBytes = 0
	o.limited = false
	o.rendered = false
}

func (o *assistantOutput) messageID(inv *invocation, thinking bool) string {
	target := &o.textID
	if thinking {
		target = &o.thoughtID
	}
	if *target == "" {
		id, err := newUUID()
		if err != nil {
			id = inv.scope.NativeInvocationID
		}
		*target = id
	}
	return *target
}

func (o *assistantOutput) acceptFrame(message *protocol.AssistantMessage) (string, bool) {
	id := message.UUID
	if id == "" {
		sum := sha256.Sum256(message.RawJSON())
		id = hex.EncodeToString(sum[:])
	}
	if len(id) > 512 || len(o.frames) >= maxRememberedIDs {
		return "", false
	}
	if _, duplicate := o.frames[id]; duplicate {
		return "", false
	}
	if o.frames == nil {
		o.frames = make(map[string]struct{})
	}
	o.frames[id] = struct{}{}
	return id, true
}

func (s *session) assistantDeltaLocked(inv *invocation, index int, value string, thinking bool) {
	if index < 0 || index >= maxUsageRows {
		return
	}
	o := &inv.output
	if o.blocks == nil {
		o.blocks = make(map[int]*assistantStreamBlock)
	}
	block := o.blocks[index]
	if block == nil {
		block = &assistantStreamBlock{segment: o.segment, id: o.messageID(inv, thinking), thinking: thinking}
		o.blocks[index] = block
	}
	if block.thinking != thinking {
		return
	}
	s.textLocked(inv, block.id, value, thinking, false)
	if !thinking && block.segment == o.segment && value != "" {
		o.rendered = true
	}
	if !block.clipped {
		if len(value) > maxTextBytes-o.streamBytes {
			block.clipped = true
		} else {
			block.prefix += value
			o.streamBytes += len(value)
		}
	}
}

func (s *session) completeAssistantBlockLocked(inv *invocation, frame, value string, thinking, aborted bool) {
	o := &inv.output
	var match *assistantStreamBlock
	index := maxUsageRows
	for n, block := range o.blocks {
		if n < index && block.thinking == thinking {
			index, match = n, block
		}
	}
	id, segment, remainder := o.messageID(inv, thinking), o.segment, value
	if match != nil {
		delete(o.blocks, index)
		o.streamBytes -= len(match.prefix)
		id, segment = match.id, match.segment
		switch {
		case match.clipped:
			remainder = ""
		case strings.HasPrefix(value, match.prefix):
			remainder = strings.TrimPrefix(value, match.prefix)
		default:
			// The complete block supersedes a conflicting stream prefix.
			// Use a fresh message ID rather than append the full text twice
			// under the ID already shown to the client.
			if segment == o.segment {
				if thinking {
					o.thoughtID = ""
				} else {
					o.textID = ""
				}
				id = o.messageID(inv, thinking)
			}
		}
	}
	s.textLocked(inv, id, remainder, thinking, !aborted)
	if thinking || segment != o.segment || value == "" {
		return
	}
	o.rendered = true
	if aborted {
		return
	}
	if len(value) > maxTextBytes-o.answerBytes || len(o.answer) >= maxUsageRows {
		o.limited = true
		return
	}
	o.answer = append(o.answer, assistantTextBlock{frame: frame, text: value})
	o.answerBytes += len(value)
}

func (s *session) assistantToolLocked(inv *invocation, block protocol.ContentBlock) {
	o := &inv.output
	if block.ID != "" && len(block.ID) <= 512 && len(o.toolBoundaries) < maxRememberedIDs {
		if _, seen := o.toolBoundaries[block.ID]; !seen {
			if o.toolBoundaries == nil {
				o.toolBoundaries = make(map[string]struct{})
			}
			o.toolBoundaries[block.ID] = struct{}{}
			o.nextSegment()
		}
	}
	s.startToolLocked(toolOwner{scope: inv.scope}, block)
}

func (o *assistantOutput) evict(ids []string) {
	o.answer = slices.DeleteFunc(o.answer, func(block assistantTextBlock) bool {
		if !slices.Contains(ids, block.frame) {
			return false
		}
		o.answerBytes -= len(block.text)
		return true
	})
	for _, id := range ids {
		if len(id) > 0 && len(id) <= 512 && len(o.frames) < maxRememberedIDs {
			if o.frames == nil {
				o.frames = make(map[string]struct{})
			}
			o.frames[id] = struct{}{}
		}
	}
}

func (o *assistantOutput) answerText() string {
	if o.limited {
		return ""
	}
	var text strings.Builder
	text.Grow(o.answerBytes)
	for _, block := range o.answer {
		text.WriteString(block.text)
	}
	return text.String()
}
