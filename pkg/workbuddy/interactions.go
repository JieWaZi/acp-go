package workbuddy

import (
	"encoding/json"
	"errors"
	"maps"

	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/userinput"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const (
	questionMethod   = "_codebuddy.ai/question"
	resolveMethod    = "_codebuddy.ai/resolveInterruption"
	maxQuestionBytes = 64 << 10
)

func prepareInitialize(request *acp.InitializeRequest) {
	capabilities := &request.ClientCapabilities
	if capabilities.Elicitation == nil || capabilities.Elicitation.Form == nil {
		return
	}
	// Older CodeBuddy builds recognize the public question extension independently of ACP Form.
	meta := make(map[string]any, len(capabilities.Meta)+1)
	maps.Copy(meta, capabilities.Meta)
	meta["codebuddy.ai"] = map[string]any{"question": true}
	capabilities.Meta = meta
}

type interactionCodec struct{}

func (interactionCodec) DecodeInteraction(method string, raw json.RawMessage) (acpclient.Interaction, error, bool) {
	var identity acpclient.CallbackIdentity
	var questions userinput.Questions
	switch method {
	case acp.ClientMethodSessionRequestPermission:
		var request acp.RequestPermissionRequest
		if json.Unmarshal(raw, &request) != nil || request.ToolCall.Meta["codebuddy.ai/toolName"] != "AskUserQuestion" {
			return acpclient.Interaction{}, nil, false
		}
		encoded, err := json.Marshal(request.ToolCall.RawInput)
		if err != nil || len(encoded) > maxQuestionBytes || json.Unmarshal(encoded, &questions) != nil {
			return invalidQuestion()
		}
		identity = acpclient.CallbackIdentity{SessionID: request.SessionId, ToolID: request.ToolCall.ToolCallId}
		// AskService indexes answers by question text in the permission path.
		for i := range questions.Questions {
			questions.Questions[i].ID = ""
		}
	case questionMethod:
		var request struct {
			// InputType discriminates the documented question payload.
			InputType string `json:"inputType"`
			// Schema holds the ordered native questions and their IDs.
			Schema userinput.Questions `json:"schema"`
		}
		var ok bool
		if len(raw) > maxQuestionBytes || json.Unmarshal(raw, &request) != nil || request.InputType != "question" {
			return invalidQuestion()
		}
		identity, ok = acpclient.DecodeCallbackIdentity(raw)
		if !ok {
			return invalidQuestion()
		}
		questions = request.Schema
		for _, question := range questions.Questions {
			if question.ID == "" {
				return invalidQuestion()
			}
		}
	default:
		return acpclient.Interaction{}, nil, false
	}
	if identity.SessionID == "" || identity.ToolID == "" {
		return invalidQuestion()
	}
	form, err := userinput.PrepareQuestions(identity.SessionID, questions)
	if err != nil {
		return invalidQuestion()
	}
	reply := questionReply{form: form, legacy: method == questionMethod}
	result := acpclient.Interaction{Identity: identity, Elicitation: &form.Request, Reply: reply}
	if !reply.legacy {
		result.Followup = &acpclient.InteractionFollowup{
			Method: resolveMethod,
			Encode: func(response acpclient.InteractionReply) (any, error) {
				answer := reply.resolve(response)
				decision := "deny"
				if answer.Action == "accept" {
					decision = "allow"
				}
				return map[string]any{"sessionId": identity.SessionID, "toolCallId": identity.ToolID, "decision": decision, "answers": answer.Answers}, nil
			},
			Validate: validateResolution,
		}
	}
	return result, nil, true
}

func (interactionCodec) DecodeProgress(string, json.RawMessage) (acpclient.Progress, error, bool) {
	return acpclient.Progress{}, nil, false
}

func invalidQuestion() (acpclient.Interaction, error, bool) {
	return acpclient.Interaction{}, acp.NewInvalidParams(nil), true
}

type questionReply struct {
	// form validates the submitted answer against the presented choices.
	form userinput.QuestionForm
	// legacy selects the extension's own reply envelope.
	legacy bool
}

func (reply questionReply) resolve(response acpclient.InteractionReply) userinput.Result {
	if response.Elicitation == nil {
		return userinput.Result{Action: "cancel"}
	}
	return reply.form.Resolve(*response.Elicitation)
}

func (reply questionReply) EncodeReply(response acpclient.InteractionReply) (any, error) {
	if !reply.legacy {
		// The answer-bearing handoff settles the question. The original approval must not allow the tool again.
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	answer := reply.resolve(response)
	if answer.Action != "accept" {
		return map[string]any{"outcome": "cancelled"}, nil
	}
	return map[string]any{"outcome": "submitted", "answers": answer.Answers}, nil
}

func validateResolution(raw json.RawMessage) error {
	var response struct {
		// Resolved confirms receipt by the native question, independently of any queue.
		Resolved bool `json:"resolved"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Resolved {
		return errors.New("CodeBuddy did not confirm question resolution")
	}
	return nil
}
