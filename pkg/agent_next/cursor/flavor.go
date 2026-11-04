package cursor

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

const observedVersion = "2026.10.22-27dcf5f"

func cursorProtocol(c *Client) acpclient.Protocol {
	return acpclient.Protocol{
		ModelScope:      acpclient.ModelPerProcess,
		PromptUsage:     acpclient.UsageSessionCumulative,
		ExecutionOption: func(category acp.SessionConfigOptionCategory) bool { return category == "model_config" },
		SessionMissing:  cursorMissingSession,
		ClassifyText:    classifyCursorText,
		Private:         privateProtocol{},
		Catalog: &acpclient.CatalogProbe{
			Method: "cursor/list_available_models", Decode: decodeModelCatalog,
			// A cold request initializes backend services before fetching models.
			Timeout: 15 * time.Second,
		},
		ParameterizedModelPicker: true,
		Authentication: acpclient.Authentication{
			Disabled: c.apiKey != "", Method: "cursor_login", RetireOnLogout: true,
			LogoutFallback: func(ctx context.Context) error {
				return c.runCommandTo(ctx, c.opts.Environment, "logout", 64<<10, io.Discard)
			},
		},
	}
}

func cursorMissingSession(err *acp.RequestError, id acp.SessionId) bool {
	details, _ := err.Data.(map[string]any)
	return err.Code == -32602 && details["message"] == fmt.Sprintf("Session %q not found", id)
}

var cursorErrorClass = regexp.MustCompile(`^[a-zA-Z0-9_$]+$`)

// Only classify complete answer chunks observed from this native release.
func classifyCursorText(version, text string) acpclient.TextClass {
	if version != observedVersion {
		return acpclient.TextAnswer
	}
	if detail, ok := strings.CutPrefix(strings.TrimSpace(text), "Error: "); ok {
		if name, rest, hasClass := strings.Cut(detail, ": "); hasClass && cursorErrorClass.MatchString(name) {
			detail = rest
		}
		if detail == "WritableIterable is closed" {
			return acpclient.TextNoise
		}
	}
	detail, ok := strings.CutPrefix(text, "\n\n")
	if ok && !strings.Contains(detail, "\n") &&
		(detail == "Please sign in to continue" || strings.HasPrefix(detail, "Error: [unauthenticated] Backend rejected authentication.")) {
		return acpclient.TextAuthenticationLost
	}
	return acpclient.TextAnswer
}
