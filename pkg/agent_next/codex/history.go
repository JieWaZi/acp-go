package codex

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	agentclient "github.com/JieWaZi/acp-go/pkg/agent_next"
	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func (c *Client) replayHistory(ctx context.Context, s *session, inline []protocol.TurnElement) error {
	if c.host.Observe == nil {
		return nil
	}
	emit := func(item protocol.ThreadItem) error {
		var updates []acp.SessionUpdate
		switch item.Type {
		case "userMessage":
			for _, part := range item.Content {
				if part.String != nil {
					updates = append(updates, acp.UpdateUserMessageText(*part.String))
					continue
				}
				if input := part.UserInput; input != nil {
					switch input.Type {
					case protocol.UserInputTypeText:
						if input.Text != nil {
							updates = append(updates, acp.UpdateUserMessageText(*input.Text))
						}
					case protocol.UserInputTypeImage:
						if input.URL != nil {
							updates = append(updates, acp.UpdateUserMessage(acp.ResourceLinkBlock("image", *input.URL)))
						}
					case protocol.LocalImage:
						if input.Path != nil {
							updates = append(updates, acp.UpdateUserMessage(acp.ResourceLinkBlock("image", "file://"+*input.Path)))
						}
					case protocol.Skill:
						if input.Name != nil && input.Path != nil {
							updates = append(updates, acp.UpdateUserMessageText(fmt.Sprintf("skill:%s (%s)", *input.Name, *input.Path)))
						}
					}
				}
			}
		case "agentMessage":
			if item.Text != nil {
				updates = append(updates, agentMessage(item.ID, *item.Text, acpmeta.MessagePhaseUnknown, true))
			}
		case "reasoning":
			for _, text := range item.Summary {
				updates = append(updates, acp.UpdateAgentThoughtText(text))
			}
		}
		for _, update := range updates {
			notification := acp.SessionNotification{SessionId: s.id, Update: update, Meta: messageMeta(item.ID, acpmeta.MessagePhaseUnknown, true)}
			if err := c.host.Observe(ctx, agentclient.Scope{SessionID: s.id}, notification); err != nil {
				return err
			}
		}
		return nil
	}
	emitTurns := func(turns []protocol.TurnElement) error {
		for _, turn := range turns {
			for _, item := range turn.Items {
				if err := emit(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if len(inline) > 0 {
		return emitTurns(inline)
	}
	params := map[string]any{"threadId": s.id, "limit": 100, "sortDirection": "asc", "itemsView": "notLoaded"}
	cursors := make(map[string]bool)
	for page := 0; page < 1000; page++ {
		var reply struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Next *string `json:"nextCursor"`
		}
		if err := c.request(ctx, "thread/turns/list", params, &reply); err != nil {
			var rpc *acp.RequestError
			if page == 0 && errors.As(err, &rpc) && rpc.Code == -32601 {
				var legacy protocol.ThreadReadResponse
				if err := c.request(ctx, "thread/read", protocol.ThreadReadParams{ThreadID: string(s.id), IncludeTurns: pointer(true)}, &legacy); err != nil {
					return err
				}
				return emitTurns(legacy.Thread.Turns)
			}
			return err
		}
		for _, turn := range reply.Data {
			if turn.ID == "" {
				return errors.New("Codex history contains an empty turn ID")
			}
			if err := c.replayHistoryItems(ctx, s.id, turn.ID, emit); err != nil {
				return err
			}
		}
		next := value(reply.Next)
		if next == "" {
			return nil
		}
		if cursors[next] {
			return errors.New("Codex repeated a turn history cursor")
		}
		cursors[next] = true
		params["cursor"] = next
	}
	return errors.New("Codex turn history exceeded page limit")
}

func (c *Client) replayHistoryItems(ctx context.Context, id acp.SessionId, turnID string, emit func(protocol.ThreadItem) error) error {
	params := map[string]any{"threadId": id, "turnId": turnID, "limit": 100, "sortDirection": "asc"}
	var cursors []string
	for page := 0; page < 1000; page++ {
		var reply struct {
			Data []struct {
				TurnID string              `json:"turnId"`
				Item   protocol.ThreadItem `json:"item"`
			} `json:"data"`
			Next *string `json:"nextCursor"`
		}
		if err := c.request(ctx, "thread/items/list", params, &reply); err != nil {
			return err
		}
		for _, entry := range reply.Data {
			if entry.TurnID != turnID {
				return errors.New("Codex history item belongs to another turn")
			}
			if err := emit(entry.Item); err != nil {
				return err
			}
		}
		next := value(reply.Next)
		if next == "" {
			return nil
		}
		if slices.Contains(cursors, next) {
			return errors.New("Codex repeated an item history cursor")
		}
		cursors = append(cursors, next)
		params["cursor"] = next
	}
	return errors.New("Codex item history exceeded page limit")
}
