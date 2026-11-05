package standardacp

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpclient"
	acp "github.com/JieWaZi/acp-go/sdk"
)

func kimiLegacyModelBehavior(environment []string, cwd string) acpclient.LegacyModelBehavior {
	home := kimiProfileHome(environment, cwd, "KIMI_SHARE_DIR", ".kimi")
	return acpclient.LegacyModelBehavior{
		ConfirmsValue:      func(version string) bool { return version == kimiPythonWireVersion },
		OpeningValueIsNoop: true,
		AppliedOnError: func(err *acp.RequestError) bool {
			if home == "" || err.Code != -32603 || err.Message != "Internal error" {
				return false
			}
			data, _ := err.Data.(map[string]any)
			detail, _ := data["details"].(string)
			path := filepath.Join(home, "config.toml")
			for _, prefix := range []string{"[Errno 13] Permission denied: ", "[Errno 30] Read-only file system: "} {
				filename, ok := strings.CutPrefix(detail, prefix)
				if ok && (filename == strconv.Quote(path) || filename == "'"+strings.ReplaceAll(path, "\\", "\\\\")+"'") {
					// The pinned setter assigns its session LLM before accessing
					// the default config path to save that model for future sessions.
					return true
				}
			}
			return false
		},
	}
}
