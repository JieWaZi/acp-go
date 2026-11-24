package standardacp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agentclient"
)

const deepSeekForkVersion = "0.2.2-rc.1"

func deepSeekSupportsFork(response acp.InitializeResponse, version string) bool {
	info := response.AgentInfo
	caps := response.AgentCapabilities.SessionCapabilities
	return slices.Contains(strings.Fields(version), deepSeekForkVersion) && info != nil && info.Name == deepSeekACPName &&
		info.Version == "0.0.1" && caps.Resume != nil && caps.Close != nil
}

// deepSeekForkHelper retains the temporary native owner until shutdown is confirmed.
type deepSeekForkHelper struct {
	// mu serializes cleanup attempts after the owning ACP operation has stopped.
	mu sync.Mutex
	// process holds the selected CLI while it can still use the overlay.
	process *agentclient.Process
	// directory is retained until the helper can no longer access it.
	directory string
}

func (h *deepSeekForkHelper) close(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.process != nil {
		if err := h.process.Close(ctx); err != nil {
			return err
		}
		h.process = nil
	}
	if h.directory != "" {
		if err := os.RemoveAll(h.directory); err != nil {
			return err
		}
		h.directory = ""
	}
	return nil
}

func (h *deepSeekForkHelper) run(ctx context.Context, executable string, options Options, request acp.UnstableForkSessionRequest) (_ acp.SessionId, resultErr error) {
	id := string(request.SessionId)
	sourceCwd, hasSourceCwd := request.Meta["sourceCwd"]
	cwd := request.Cwd
	if hasSourceCwd {
		cwd, _ = sourceCwd.(string)
	}
	sequence, position, _ := strings.Cut(acpmeta.ForkPosition(request.Meta), ":")
	turnNumber, userID, _ := strings.Cut(position, ":")
	boundary, boundaryErr := strconv.ParseInt(sequence, 10, 64)
	turn, turnErr := strconv.ParseInt(turnNumber, 10, 64)
	if !validDeepSeekSessionID(id) || boundaryErr != nil || turnErr != nil ||
		boundary <= 0 || boundary > maxExactMetadataCounter || turn <= 0 || turn > maxExactMetadataCounter ||
		!validDeepSeekSessionID(userID) || !sameDeepSeekDirectory(cwd, request.Cwd) {
		return "", errors.New("DeepSeek Harness fork requires an exact terminal boundary in the same physical workspace")
	}
	ctx, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err := h.close(ctx); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp("", "acp-go-dsh-fork-")
	if err != nil {
		return "", errors.New("cannot create the DeepSeek Harness fork overlay")
	}
	h.directory = directory
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, h.close(cleanup))
	}()
	plugin := filepath.Join(directory, "fork.mjs")
	overlay := filepath.Join(directory, "fork.json")
	patch, err := json.Marshal([]map[string]any{
		{"id": "acp", "disabled": true},
		{"insert": []map[string]string{{"id": "acp-go-native-fork", "name": plugin}}},
	})
	if err != nil || os.WriteFile(plugin, []byte(deepSeekForkPlugin), 0600) != nil || os.WriteFile(overlay, patch, 0600) != nil {
		return "", errors.New("cannot write the DeepSeek Harness fork overlay")
	}
	args := append(slices.Clone(options.PrefixArgs), "--profile", "acp", "--patch", overlay)
	process, err := agentclient.NewProcess(agentclient.ProcessSpec{
		Executable: executable, Args: args, Env: options.Environment, Dir: options.WorkingDirectory,
	})
	if err != nil {
		return "", err
	}
	h.process = process
	if err := process.Start(ctx); err != nil {
		return "", err
	}
	input := process.Stdin()
	output := process.Stdout()
	stopIO := context.AfterFunc(ctx, func() {
		_ = input.Close()
		_ = output.Close()
	})
	defer stopIO()
	nonce := rand.Text()
	sendErr := json.NewEncoder(input).Encode(map[string]any{
		"source": id, "cwd": cwd, "nonce": nonce, "boundary": boundary, "turn": turn, "user": userID,
	})
	if err := errors.Join(sendErr, input.Close()); err != nil {
		return "", errors.New("cannot send the DeepSeek Harness fork request")
	}
	line, err := bufio.NewReaderSize(output, 4096).ReadSlice('\n')
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var receipt struct {
		// Source confirms the exact native source requested by the host.
		Source string `json:"source"`
		// Child identifies a durably flushed independent branch.
		Child string `json:"child"`
		// Boundary is the inclusive native source position.
		Boundary *int64 `json:"boundary"`
		// Nonce proves the receipt belongs to this invocation.
		Nonce string `json:"nonce"`
	}
	if err != nil || json.Unmarshal(line, &receipt) != nil || receipt.Source != id || receipt.Nonce != nonce ||
		receipt.Child == id || !validDeepSeekSessionID(receipt.Child) || receipt.Boundary == nil || *receipt.Boundary != boundary {
		return "", errors.New("DeepSeek Harness did not confirm a durable fork")
	}
	return acp.SessionId(receipt.Child), nil
}

func validDeepSeekSessionID(id string) bool {
	return len(id) > 0 && len(id) <= 250 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\r\n\x00")
}

func sameDeepSeekDirectory(left, right string) bool {
	if !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	first, err := os.Stat(left)
	if err != nil || !first.IsDir() {
		return false
	}
	second, err := os.Stat(right)
	return err == nil && second.IsDir() && os.SameFile(first, second)
}

// The overlay keeps the selected ACP profile and its persistence provider.
// It replaces the stdio server with one call to DSH's public session APIs.
const deepSeekForkPlugin = `
import { randomUUID } from "node:crypto";
import { readFileSync, writeSync } from "node:fs";
import { realpath } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

export const name = "acp-go-native-fork";
export const inject = ["sessions", "sessionPersistence", "sessionQuery", "agents", "agentDefaultModel"];

export async function apply(ctx) {
  try {
    const request = JSON.parse(readFileSync(0, "utf8"));
    const launcher = await realpath(process.argv[1]);
    const manifest = JSON.parse(readFileSync(join(dirname(launcher), "..", "package.json"), "utf8"));
    if (manifest.name !== "@deepseek-ai/dsh" || manifest.version !== "0.2.2-rc.1") return;
    const nativeRequire = createRequire(launcher);
    const { buildForkSeed } = await import(pathToFileURL(nativeRequire.resolve("@deepseek-ai/dsh-session/fork")));
    const signal = AbortSignal.timeout(10_000);
    const cwd = await realpath(request.cwd);
    const snapshot = await ctx.sessionPersistence.stat(request.source, { signal });
    const matches = async (header) =>
      header?.id === request.source && header.origin !== "subagent" &&
      header.version === 4 && await realpath(header.cwd) === cwd;
    if (!(await matches(snapshot?.header)) || snapshot.formatStatus !== "current" ||
        !Number.isSafeInteger(snapshot.sizeBytes) || snapshot.sizeBytes > 64 * 1024 * 1024) return;
    using source = await ctx.sessionQuery.observeSession(request.source, { signal, projectionMode: "none" });
    if (!(await matches(source.header)) || source.revision !== snapshot.revision) return;
    const events = source.events;
    const boundary = request.boundary;
    const terminal = events[boundary];
    if (terminal?.seq !== boundary || terminal.type !== "turn/end" || terminal.data.turn !== request.turn ||
        !["completed", "max-tokens"].includes(terminal.data.reason.kind)) return;
    let start = boundary - 1;
    for (; start >= 0 && events[start].type !== "turn/start"; start--) {}
    if (start < 0 || events[start].data.turn !== request.turn) return;
    const users = events.slice(start + 1, boundary).filter((event) =>
      event.type === "user/message" && event.data.role === "user" && event.data.source.kind === "user");
    if (users.length !== 1 || users[0].data.id !== request.user) return;
    const child = randomUUID();
    const handle = await ctx.agents.create({
      sessionId: child, seed: buildForkSeed(events, boundary), inheritedEventCount: boundary + 1,
      meta: { cwd: source.header.cwd, isSeeded: true, agentPreset: source.header.agentPreset },
      agentOptions: ctx.agentDefaultModel.currentSelection(), signal
    });
    try {
      await ctx.sessions.flush(handle.agent.session);
    } finally {
      await handle.dispose();
    }
    writeSync(1, JSON.stringify({ source: request.source, child, boundary, nonce: request.nonce }) + "\n");
  } catch {
    process.exitCode = 1;
  }
}
`
