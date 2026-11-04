package agentclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProcessChild runs the actual provider-side fixture over inherited pipes.
func TestProcessChild(t *testing.T) {
	args := os.Args
	if len(args) >= 4 && args[2] == "--" && args[3] == "batch" {
		_ = json.NewEncoder(os.Stdout).Encode(args[4:])
		os.Exit(0)
	}
	if len(args) < 3 || args[len(args)-2] != "--" {
		return
	}
	switch args[len(args)-1] {
	case "exit":
		fmt.Fprint(os.Stdout, "{\"final\":true}\n")
		fmt.Fprint(os.Stderr, "fixture failure")
		os.Exit(19)
	case "inspect":
		dir, err := os.Getwd()
		if err != nil {
			os.Exit(33)
		}
		_ = json.NewEncoder(os.Stdout).Encode([]string{dir, os.Getenv("AGENT_NEXT_TEST_FREEZE")})
		os.Exit(0)
	case "echo":
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Fprint(os.Stdout, line)
		os.Exit(0)
	case "graceful":
		ending := make(chan os.Signal, 1)
		signal.Notify(ending, syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		<-ending
		if err := os.WriteFile(os.Getenv("AGENT_NEXT_TEST_FLUSH"), []byte("flushed"), 0600); err != nil {
			os.Exit(35)
		}
		os.Exit(0)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(25 * time.Second)
		os.Exit(36)
	case "broken-inspect":
		fmt.Fprint(os.Stderr, "broken inspect {\n  token: 'synthetic',\nFATAL ERROR: actionable crash\nlatest safe tail\n")
		os.Exit(17)
	case "nested-inspect":
		fmt.Fprint(os.Stderr, os.Getenv("AGENT_NEXT_TEST_DIAGNOSTIC"))
		os.Exit(18)
	case "diagnostic":
		fmt.Fprint(os.Stderr, "FATAL ERROR: startup failed\n")
		fmt.Fprint(os.Stderr, "fixture {\n  token: '\x1b[31mse")
		fmt.Fprint(os.Stderr, "cret-value\x1b[0m',\n}\n")
		fmt.Fprint(os.Stderr, "long-token="+strings.Repeat("a", 100000)+"DO-NOT-LEAK\n")
		fmt.Fprint(os.Stderr, "payload {\n  token: '"+strings.Repeat("b", 100000)+"',\n  continuation: 'DO-NOT-LEAK',\n}\n")
		fmt.Fprint(os.Stderr, "inline { token: 'se\ncret-value',\n  payload: 'continuation',\n}\n")
		fmt.Fprint(os.Stderr, strings.Repeat("x", 100000)+"openssl -----BEGIN PRIVATE KEY-----\nDO-NOT-LEAK\n-----END PRIVATE KEY-----\n")
		fmt.Fprint(os.Stderr, "latest safe tail")
		os.Exit(21)
	case "descendant":
		child := exec.Command(args[0], "-test.run=^TestProcessChild$", "--", "hold")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(31)
		}
		fmt.Fprintln(os.Stdout, "leader exited")
		os.Exit(0)
	case "running-tree":
		child := exec.Command(args[0], "-test.run=^TestProcessChild$", "--", "hold")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(31)
		}
		fmt.Fprintln(os.Stdout, "tree ready")
		time.Sleep(25 * time.Second)
		os.Exit(32)
	case "hold":
		time.Sleep(25 * time.Second)
		os.Exit(32)
	default:
		os.Exit(34)
	}
}

func processFixture(t *testing.T, mode string) ProcessSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ProcessSpec{Executable: exe, Args: []string{"-test.run=^TestProcessChild$", "--", mode}}
}

func processTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newProcessFixture(t *testing.T, spec ProcessSpec) *Process {
	t.Helper()
	p, err := NewProcess(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return p
}

func startProcessFixture(t *testing.T, ctx context.Context, spec ProcessSpec) *Process {
	t.Helper()
	p := newProcessFixture(t, spec)
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return p
}

func readProcessOutput(t *testing.T, ctx context.Context, p *Process) string {
	t.Helper()
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := io.ReadAll(p.Stdout())
		done <- result{output, err}
	}()
	select {
	case output := <-done:
		if output.err != nil {
			t.Fatal(output.err)
		}
		return string(output.output)
	case <-ctx.Done():
		t.Fatal("stdout never reached EOF")
		return ""
	}
}

func TestProcessRetainsFinalStdoutAfterNonzeroExit(t *testing.T) {
	ctx := processTestContext(t)
	spec := processFixture(t, "exit")
	spec.Redact = RedactProcessDiagnostic
	p := startProcessFixture(t, ctx, spec)
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatal("process was not reaped")
	}
	exit, err := p.Wait(ctx)
	if err != nil || exit.Code != 19 || exit.Err == nil || exit.Diagnostic != "fixture failure" {
		t.Fatalf("native outcome: %+v, %v", exit, err)
	}
	if output := readProcessOutput(t, ctx, p); output != "{\"final\":true}\n" {
		t.Fatalf("lost the trailing frame: %q", output)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatalf("native exit code is not a cleanup error: %v", err)
	}
}

func TestProcessFreezesLaunchBeforeStart(t *testing.T) {
	ctx := processTestContext(t)
	spec := processFixture(t, "inspect")
	spec.Dir = t.TempDir()
	t.Setenv("AGENT_NEXT_TEST_FREEZE", "inherited-before")
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			t.Setenv("AGENT_NEXT_TEST_FREEZE", "inherited-before")
			launch := spec
			launch.Args = append([]string(nil), spec.Args...)
			want := "inherited-before"
			if explicit {
				want = "explicit-before"
				launch.Executable = filepath.Base(spec.Executable)
				launch.Env = []string{"AGENT_NEXT_TEST_FREEZE=" + want, "PATH=" + filepath.Dir(spec.Executable)}
			}
			p := newProcessFixture(t, launch)
			if p.Executable() != spec.Executable {
				t.Fatalf("frozen executable resolution did not use the launch PATH: %q", p.Executable())
			}
			t.Setenv("AGENT_NEXT_TEST_FREEZE", "inherited-after")
			launch.Args[2] = "hold"
			if explicit {
				launch.Env[0] = "AGENT_NEXT_TEST_FREEZE=explicit-after"
				launch.Env[1] = "PATH="
			}
			launch.Dir = t.TempDir()
			if err := p.Start(ctx); err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := json.Unmarshal([]byte(readProcessOutput(t, ctx, p)), &got); err != nil {
				t.Fatal(err)
			}
			dir, err := filepath.EvalSymlinks(spec.Dir)
			if err != nil || len(got) != 2 || got[0] != dir || got[1] != want {
				t.Fatalf("launch was not frozen: %q; want [%q %q], %v", got, dir, want, err)
			}
		})
	}
}

func TestProcessStartContextDoesNotOwnChildLifetime(t *testing.T) {
	ctx := processTestContext(t)
	launchCtx, cancel := context.WithCancel(ctx)
	p := startProcessFixture(t, launchCtx, processFixture(t, "echo"))
	cancel()
	if _, err := io.WriteString(p.Stdin(), "still alive\n"); err != nil {
		t.Fatal(err)
	}
	if got := readProcessOutput(t, ctx, p); got != "still alive\n" {
		t.Fatalf("child died with the completed launch context: %q", got)
	}
}

func TestProcessCloseCanRetrySameOwnerAfterTimeout(t *testing.T) {
	ctx := processTestContext(t)
	entered, unblock := make(chan struct{}), make(chan struct{})
	spec := processFixture(t, "exit")
	spec.Redact = func(record string) string {
		close(entered)
		<-unblock
		return record
	}
	p := startProcessFixture(t, ctx, spec)
	defer func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("fixture did not reach diagnostic drain")
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := p.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup should time out separately from native exit: %v", err)
	}
	close(unblock)
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx); err == nil {
		t.Fatal("the same owner restarted")
	}
	next := startProcessFixture(t, ctx, processFixture(t, "echo"))
	if err := p.Close(ctx); err != nil {
		t.Fatalf("idempotent close of the previous owner: %v", err)
	}
	if _, err := io.WriteString(next.Stdin(), "next owner\n"); err != nil {
		t.Fatal(err)
	}
	if got := readProcessOutput(t, ctx, next); got != "next owner\n" {
		t.Fatalf("closing the old owner interfered with the new owner: %q", got)
	}
}

func TestProcessKillsDescendantsAfterLeaderExit(t *testing.T) {
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, processFixture(t, "descendant"))
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatal("leader was not reaped")
	}
	if got := readProcessOutput(t, ctx, p); got != "leader exited\n" {
		t.Fatalf("descendant or cleanup corrupted stdout: %q", got)
	}
	exit, err := p.Wait(ctx)
	if err != nil || exit.Code != 0 {
		t.Fatalf("did not finish diagnostic drain for the dead group: %+v, %v", exit, err)
	}
}

func TestProcessCloseStopsActiveTree(t *testing.T) {
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, processFixture(t, "running-tree"))
	output := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(p.Stdout()).ReadString('\n')
		output <- line
	}()
	select {
	case line := <-output:
		if line != "tree ready\n" {
			t.Fatalf("fixture did not start its descendant: %q", line)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := p.Close(ctx); err != nil {
		t.Fatalf("close did not stop and drain the active tree: %v", err)
	}
	exit, err := p.Wait(ctx)
	if err != nil || exit.Err == nil {
		t.Fatalf("stopped child lost its native exit: %+v, %v", exit, err)
	}
}

func TestProcessFailedStartReleasesPartialPipes(t *testing.T) {
	ctx := processTestContext(t)
	spec := processFixture(t, "exit")
	spec.Dir = filepath.Join(t.TempDir(), "missing-private-dir")
	// On Unix, compare the live descriptors as well as the public handles.
	before, listErr := os.ReadDir("/dev/fd")
	p := newProcessFixture(t, spec)
	err := p.Start(ctx)
	if err == nil || strings.Contains(err.Error(), "missing-private-dir") {
		t.Fatalf("start failure is missing or unsafe: %v", err)
	}
	if p.Stdout() != nil || p.Stdin() != nil {
		t.Fatal("failed launch published live streams")
	}
	if err := p.Close(ctx); err != nil {
		t.Fatalf("failed launch left resources behind: %v", err)
	}
	if listErr == nil {
		after, err := os.ReadDir("/dev/fd")
		if err != nil || len(after) != len(before) {
			t.Fatalf("failed launch changed descriptor count from %d to %d: %v", len(before), len(after), err)
		}
	}
}

func TestProcessRedactsWholeRecordsAndDropsOverflow(t *testing.T) {
	ctx := processTestContext(t)
	spec := processFixture(t, "diagnostic")
	var redactorInput []string
	spec.Redact = func(record string) string {
		redactorInput = append(redactorInput, record)
		return strings.ReplaceAll(record, "secret-value", "[redacted]")
	}
	p := startProcessFixture(t, ctx, spec)
	exit, err := p.Wait(ctx)
	if err != nil || exit.Diagnostic != "FATAL ERROR: startup failed" || exit.Tail != "latest safe tail" {
		t.Fatalf("missing preferred crash and latest tail: %+v, %v", exit, err)
	}
	want := []string{"FATAL ERROR: startup failed", "fixture {\n  token: 'secret-value',\n}", "inline { token: 'se\ncret-value',\n  payload: 'continuation',\n}", "latest safe tail"}
	if fmt.Sprint(redactorInput) != fmt.Sprint(want) {
		t.Fatalf("sanitizer received a partial record, overflow fragment, or terminal escape: %q", redactorInput)
	}
}

func TestProcessSuppressesRawDiagnosticsWithoutRedactor(t *testing.T) {
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, processFixture(t, "diagnostic"))
	exit, err := p.Wait(ctx)
	if err != nil || exit.Diagnostic != "" || exit.Tail != "" {
		t.Fatalf("raw diagnostics escaped: %+v, %v", exit, err)
	}
}

func TestProcessCloseAllowsNativeFlush(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows closes the owned job without a POSIX grace signal")
	}
	ctx := processTestContext(t)
	marker := filepath.Join(t.TempDir(), "flushed")
	spec := processFixture(t, "graceful")
	spec.Env = append(os.Environ(), "AGENT_NEXT_TEST_FLUSH="+marker)
	p := startProcessFixture(t, ctx, spec)
	line, err := bufio.NewReader(p.Stdout()).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("native flush handler was not ready: %q, %v", line, err)
	}
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	exit, err := p.Wait(ctx)
	if err != nil || exit.Code != 0 || exit.Signal != "" {
		t.Fatalf("native flush was interrupted: %+v, %v", exit, err)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "flushed" {
		t.Fatalf("native persistence was skipped: %q, %v", body, err)
	}
}

func TestProcessCloseAtNativeEOF(t *testing.T) {
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, processFixture(t, "exit"))
	readProcessOutput(t, ctx, p)
	if err := p.Close(ctx); err != nil {
		t.Fatalf("closing at native EOF misclassified exit as a cleanup failure: %v", err)
	}
	exit, err := p.Wait(ctx)
	if err != nil || exit.Code != 19 {
		t.Fatalf("cleanup overwrote the native exit: %+v, %v", exit, err)
	}
}

func TestProcessCloseEscalatesUnresponsiveChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses job termination")
	}
	ctx := processTestContext(t)
	p := startProcessFixture(t, ctx, processFixture(t, "ignore-term"))
	line, err := bufio.NewReader(p.Stdout()).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("signal policy was not ready: %q, %v", line, err)
	}
	started := time.Now()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
	exit, err := p.Wait(ctx)
	if err != nil || exit.Signal != "killed" || time.Since(started) < 1900*time.Millisecond {
		t.Fatalf("stop did not honor the grace window before escalation: %+v, %v", exit, err)
	}
}

func TestProcessRecoversDiagnosticAfterTruncatedInspect(t *testing.T) {
	ctx := processTestContext(t)
	spec := processFixture(t, "broken-inspect")
	spec.Redact = RedactProcessDiagnostic
	p := startProcessFixture(t, ctx, spec)
	exit, err := p.Wait(ctx)
	if err != nil || exit.Code != 17 || exit.Diagnostic != "FATAL ERROR: actionable crash" || exit.Tail != "latest safe tail" {
		t.Fatalf("truncated inspect swallowed independent crash records: %+v, %v", exit, err)
	}
}

func TestProcessSharedRedactorOmitsCredentialRecords(t *testing.T) {
	for _, unsafe := range []string{
		"Authorization: Bearer unknown-value",
		`reply {"apiKey":"unknown-value"}`,
		"open https://example.invalid/login?code=unknown-value",
		"failed https://name:password@example.invalid/endpoint",
		"node inspect {\n  token: 'unknown-value',\n}",
		"PRIVATE KEY-----",
	} {
		if got := RedactProcessDiagnostic(unsafe); got != "" {
			t.Errorf("credential-like record was retained: %q", got)
		}
	}
	if got := RedactProcessDiagnostic("authentication failed: sign in again"); got != "authentication failed: sign in again" {
		t.Errorf("safe diagnostic was lost: %q", got)
	}
}

func TestProcessDiagnosticRecordRecoveryAcrossChunks(t *testing.T) {
	for _, prefix := range []string{
		"inspect {\n  token: 'synthetic',\n",
		"inspect {\n  token: 'unterminated\n",
		"inspect {\n  payload: '" + strings.Repeat("x", processRecordLimit) + "\n",
	} {
		for _, stride := range []int{1, 7, 4096} {
			var records []string
			parser := processRecords{emit: func(record string) { records = append(records, record) }}
			body := prefix + "FATAL ERROR: recovered\nlast safe record"
			for offset := 0; offset < len(body); offset += stride {
				end := min(offset+stride, len(body))
				_, _ = parser.Write([]byte(body[offset:end]))
			}
			parser.finish()
			if len(records) != 2 || records[0] != "FATAL ERROR: recovered" || records[1] != "last safe record" {
				t.Fatalf("stride %d lost recovery or exposed incomplete payload: %q", stride, records)
			}
		}
	}
	var records []string
	parser := processRecords{emit: func(record string) { records = append(records, record) }}
	jsonRecord := "json {\n\"note\": \"it's one payload\",\n\"nested\": {\"ok\":true}\n}"
	for _, b := range []byte(jsonRecord + "\nlast") {
		_, _ = parser.Write([]byte{b})
	}
	parser.finish()
	if len(records) != 2 || records[0] != jsonRecord || records[1] != "last" {
		t.Fatalf("unindented JSON was incorrectly split as inspect: %q", records)
	}
}

func TestProcessKeepsNestedInspectAsOneCredentialRecord(t *testing.T) {
	for _, quote := range []string{"'", "`"} {
		body := "[\n  {\n    token: " + quote + "synthetic-}}-key" + quote + ",\n    refresh: " + quote + "private-probe-marker" + quote
		complete := body + "\n  }\n]"
		for _, value := range []string{body, complete} {
			ctx := processTestContext(t)
			spec := processFixture(t, "nested-inspect")
			spec.Env = append(os.Environ(), "AGENT_NEXT_TEST_DIAGNOSTIC="+value)
			spec.Redact = RedactProcessDiagnostic
			p := startProcessFixture(t, ctx, spec)
			exit, err := p.Wait(ctx)
			if err != nil || exit.Code != 18 || exit.Tail != "" || exit.Diagnostic != "" {
				t.Fatalf("nested structure leaked a fragment before redaction: %+v, %v", exit, err)
			}
		}
		for _, stride := range []int{1, 7, len(complete)} {
			var records []string
			parser := processRecords{emit: func(record string) { records = append(records, record) }}
			input := complete + "\nsafe tail"
			for i := 0; i < len(input); i += stride {
				_, _ = parser.Write([]byte(input[i:min(i+stride, len(input))]))
			}
			parser.finish()
			if len(records) != 2 || records[0] != complete || records[1] != "safe tail" {
				t.Fatalf("stride %d split a nested record before redaction: %q", stride, records)
			}
		}
	}
}
