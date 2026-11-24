package standardacp

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
)

const deepSeekACPName = "deepseek-harness-acp"

func configureDeepSeek(options Options, protocol *acpclient.Protocol) ([]string, error) {
	if options.PermissionMode != "" && options.PermissionMode != "default" {
		return nil, errors.New("DeepSeek Harness ACP has no session permission-mode setter")
	}
	protocol.Authentication.Disabled = true
	protocol.BlockUntypedExtensions = true
	protocol.NormalizeOption = func(option acp.SessionConfigOption) acp.SessionConfigOption {
		if option.Select != nil && option.Select.Id == "reasoning_effort" {
			option.Select.Category = acp.Ptr(acp.SessionConfigOptionCategoryThoughtLevel)
		}
		return option
	}
	protocol.ForkAllowed = func(acp.InitializeResponse) bool { return false }
	protocol.SupportsNativeFork = deepSeekSupportsFork
	options.Environment = slices.Clone(options.Environment)
	options.PrefixArgs = slices.Clone(options.PrefixArgs)
	forkHelper := &deepSeekForkHelper{}
	protocol.CloseAuxiliary = forkHelper.close
	protocol.ForkNativeSession = func(ctx context.Context, client *acpclient.Client, request acp.UnstableForkSessionRequest) (acp.SessionId, error) {
		return forkHelper.run(ctx, client.Executable(), options, request)
	}
	protocol.PromptUsage = acpclient.UsageUnavailable
	protocol.SessionMissing = func(err *acp.RequestError, id acp.SessionId) bool {
		return err.Code == -32602 && err.Message == "Invalid params: session is not resumable: "+string(id)
	}
	home := deepSeekHome(options.Environment, options.WorkingDirectory)
	protocol.CapturePromptJournal = func(ctx context.Context, scope acpclient.PromptJournalScope) func(context.Context, acpclient.PromptJournalReceipt) acpclient.PromptJournalEvidence {
		return captureDeepSeekJournal(ctx, scope, home)
	}
	args := []string{"--profile", "acp"}
	if patch := options.DeepSeekCatalogPatch; patch != "" {
		if !filepath.IsAbs(patch) {
			return nil, errors.New("DeepSeek catalog discovery requires an absolute overlay path")
		}
		// The overlay owns the discovery journal. The CLI can remove a deprecated
		// opening model from its options after selecting another model.
		protocol.DisposableModelCatalog = true
		args = append([]string{"--patch", patch}, args...)
	}
	return args, nil
}

func deepSeekHome(environment []string, cwd string) string {
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	userHome := environmentValue(environment, homeKey)
	home := environmentValue(environment, "DSH_HOME")
	switch {
	case strings.TrimSpace(home) == "":
		if !filepath.IsAbs(userHome) {
			return ""
		}
		return filepath.Join(userHome, ".dsh")
	case home == "~", strings.HasPrefix(home, "~/"), strings.HasPrefix(home, `~\`):
		if !filepath.IsAbs(userHome) {
			return ""
		}
		return filepath.Join(userHome, strings.TrimLeft(strings.TrimPrefix(home, "~"), `/\`))
	case !filepath.IsAbs(home):
		absolute, err := filepath.Abs(cwd)
		if err != nil {
			return ""
		}
		return filepath.Join(absolute, home)
	default:
		return filepath.Clean(home)
	}
}
