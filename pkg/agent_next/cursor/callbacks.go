package cursor

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type privateProtocol struct{}

const (
	askQuestion   = "cursor/ask_question"
	createPlan    = "cursor/create_plan"
	updateTodos   = "cursor/update_todos"
	updateTask    = "cursor/task"
	generateImage = "cursor/generate_image"
)

type questionOption struct {
	// ID is the exact Cursor choice returned with the answer.
	ID string `json:"id"`
	// Label is the provider's display text.
	Label string `json:"label"`
}

type question struct {
	// ID is the provider identity returned in questionId.
	ID string `json:"id"`
	// Prompt is the provider question text.
	Prompt string `json:"prompt"`
	// Options is the ordered native choice list.
	Options []questionOption `json:"options"`
	// Multiple permits more than one selected ID.
	Multiple bool `json:"allowMultiple"`
}

type privateInteraction struct {
	// Title and Questions describe the native form.
	Title     string     `json:"title"`
	Questions []question `json:"questions"`
	// Name and Plan describe native plan approval.
	Name string `json:"name"`
	Plan string `json:"plan"`
}

func (privateProtocol) DecodeInteraction(method string, raw json.RawMessage) (acpclient.Interaction, error, bool) {
	if method != askQuestion && method != createPlan {
		return acpclient.Interaction{}, nil, false
	}
	identity, valid := acpclient.DecodeCallbackIdentity(raw)
	var request privateInteraction
	if !valid || json.Unmarshal(raw, &request) != nil {
		return acpclient.Interaction{}, acp.NewInvalidParams(nil), true
	}
	result := acpclient.Interaction{Identity: identity}
	if method == createPlan {
		title := request.Name
		if strings.TrimSpace(title) == "" {
			title = "Review plan"
		}
		result.Permission = &acp.RequestPermissionRequest{
			ToolCall: acp.ToolCallUpdate{ToolCallId: identity.ToolID, Title: &title, Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(request.Plan))}},
			Options: []acp.PermissionOption{
				{OptionId: "accept", Name: "Accept plan", Kind: acp.PermissionOptionKindAllowOnce},
				{OptionId: "reject", Name: "Reject plan", Kind: acp.PermissionOptionKindRejectOnce},
			},
		}
		result.Reply = planReply{}
	} else {
		form, err := questionForm(identity.ToolID, request)
		if err != nil {
			return acpclient.Interaction{}, err, true
		}
		result.Elicitation = form
		result.Reply = questionReply(request.Questions)
	}
	return result, nil, true
}

type planReply struct{}

func (planReply) EncodeReply(reply acpclient.InteractionReply) (any, error) {
	if reply.Permission != nil && reply.Permission.Outcome.Selected != nil {
		switch reply.Permission.Outcome.Selected.OptionId {
		case "accept":
			return cursorDecision("accepted"), nil
		case "reject":
			return cursorDecision("rejected"), nil
		}
	}
	return cursorDecision("cancelled"), nil
}

func questionForm(toolID acp.ToolCallId, request privateInteraction) (*acp.UnstableCreateElicitationRequest, error) {
	if len(request.Questions) == 0 || len(request.Questions) > 64 {
		return nil, acp.NewInvalidParams(nil)
	}
	properties := make(map[string]any, len(request.Questions))
	required := make([]string, 0, len(request.Questions))
	for _, q := range request.Questions {
		if q.ID == "" || len(q.Options) == 0 || len(q.Options) > 128 || properties[q.ID] != nil {
			return nil, acp.NewInvalidParams(nil)
		}
		ids, labels := make([]string, 0, len(q.Options)), make([]string, 0, len(q.Options))
		for _, option := range q.Options {
			if option.ID == "" || slices.Contains(ids, option.ID) {
				return nil, acp.NewInvalidParams(nil)
			}
			ids, labels = append(ids, option.ID), append(labels, option.Label)
		}
		choices := map[string]any{"type": "string", "enum": ids, "enumNames": labels, "title": q.Prompt}
		if q.Multiple {
			choices = map[string]any{"type": "array", "title": q.Prompt, "items": choices, "minItems": 1, "uniqueItems": true}
		}
		properties[q.ID] = choices
		required = append(required, q.ID)
	}
	title := request.Title
	if strings.TrimSpace(title) == "" {
		title = "Input requested"
	}
	return &acp.UnstableCreateElicitationRequest{Form: &acp.UnstableCreateElicitationForm{
		Mode: "form", Message: title, Meta: map[string]any{"toolCallId": string(toolID)},
		RequestedSchema: acp.UnstableElicitationSchema{Properties: properties, Required: required},
	}}, nil
}

type questionReply []question

func (questions questionReply) EncodeReply(reply acpclient.InteractionReply) (any, error) {
	response := reply.Elicitation
	if response == nil {
		return cursorDecision("cancelled"), nil
	}
	if response.Decline != nil {
		return cursorDecision("skipped"), nil
	}
	if response.Accept == nil {
		return cursorDecision("cancelled"), nil
	}
	answers := make([]map[string]any, 0, len(questions))
	for _, q := range questions {
		values := selectedAnswers(response.Accept.Content[q.ID])
		if len(values) == 0 || !q.Multiple && len(values) != 1 {
			return cursorDecision("cancelled"), nil
		}
		seen := make(map[string]bool, len(values))
		for _, id := range values {
			if seen[id] || !slices.ContainsFunc(q.Options, func(option questionOption) bool { return option.ID == id }) {
				return cursorDecision("cancelled"), nil
			}
			seen[id] = true
		}
		answers = append(answers, map[string]any{"questionId": q.ID, "selectedOptionIds": values})
	}
	return map[string]any{"outcome": map[string]any{"outcome": "answered", "answers": answers}}, nil
}

func selectedAnswers(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil
			}
			values = append(values, text)
		}
		return values
	default:
		return nil
	}
}

func cursorDecision(value string) map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": value}}
}

type todoEntry struct {
	// ID is the native merge key.
	ID string `json:"id"`
	// Text is the task description.
	Text string `json:"content"`
	// Status is the native task state.
	Status string `json:"status"`
}

type privateProgress struct {
	// Todos and Merge represent a snapshot or incremental update.
	Todos []todoEntry `json:"todos"`
	Merge bool        `json:"merge"`
	// Description and FilePath describe a task or image.
	Description string `json:"description"`
	FilePath    string `json:"filePath"`
}

func (privateProtocol) DecodeProgress(method string, raw json.RawMessage) (acpclient.Progress, error, bool) {
	if method != updateTodos && method != updateTask && method != generateImage {
		return acpclient.Progress{}, nil, false
	}
	identity, valid := acpclient.DecodeCallbackIdentity(raw)
	var wire privateProgress
	if !valid || json.Unmarshal(raw, &wire) != nil {
		return acpclient.Progress{}, acp.NewInvalidParams(nil), true
	}
	result := acpclient.Progress{Identity: identity}
	if method == updateTodos {
		limit := min(len(wire.Todos), acpclient.MaxPlanEntries+1)
		plan := &acpclient.PlanProgress{Merge: wire.Merge, Entries: make([]acpclient.PlanItem, 0, limit)}
		for _, todo := range wire.Todos[:limit] {
			status, text := acp.PlanEntryStatus(todo.Status), todo.Text
			if todo.Status == "cancelled" {
				status, text = acp.PlanEntryStatusCompleted, "[Cancelled] "+text
			}
			item := acpclient.PlanItem{ID: todo.ID}
			switch status {
			case acp.PlanEntryStatusPending, acp.PlanEntryStatusInProgress, acp.PlanEntryStatusCompleted:
				item.Entry = &acp.PlanEntry{Status: status, Content: text, Priority: acp.PlanEntryPriorityMedium}
			}
			plan.Entries = append(plan.Entries, item)
		}
		result.Plan = plan
	} else if identity.ToolID != "" {
		var locations []acp.ToolCallLocation
		if wire.FilePath != "" {
			locations = []acp.ToolCallLocation{{Path: wire.FilePath}}
		}
		update := acp.UpdateToolCall(identity.ToolID, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(wire.Description))}), acp.WithUpdateLocations(locations))
		result.Update = &update
	}
	return result, nil, true
}
