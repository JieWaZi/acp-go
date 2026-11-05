package agentclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProcessWindowsBatchLaunchesWithExactArgv(t *testing.T) {
	ctx := processTestContext(t)
	spec := processFixture(t, "batch")
	fixture := spec.Executable
	// Exercise the extra parser used by npm's local forwarding shims.
	dir := filepath.Join(t.TempDir(), "Program Files (x86)", "node_modules", ".bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec.Executable = filepath.Join(dir, "tool.cmd")
	if err := os.WriteFile(spec.Executable, []byte("@echo off\r\n\"%ACP_PROCESS_TEST_EXE%\" -test.run=^TestProcessChild$ -- batch %*\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec.Env = append(os.Environ(), "ACP_PROCESS_TEST_EXE="+fixture)
	spec.Args = []string{"--model", "a&b", `say "hi"`, `C:\work\`, "", `{"mcp":"value"}`}
	p := startProcessFixture(t, ctx, spec)
	var got []string
	if err := json.Unmarshal([]byte(readProcessOutput(t, ctx, p)), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, spec.Args) {
		t.Fatalf("batch shim changed argument boundaries: %q != %q", got, spec.Args)
	}
}

func TestProcessWindowsBatchQuotingRejectsUnsafeDelimiters(t *testing.T) {
	path := `C:\Program Files (x86)\node_modules\.bin\tool.cmd`
	line, err := processBatchCommandLine(path, []string{"a&b", `say "hi"`, `C:\work\`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Program^ Files^ ^(x86^)\node_modules\.bin\tool.cmd`, `a^^^&b`, `say^^^ \`, `C:\work\\`} {
		if !strings.Contains(line, want) {
			t.Errorf("missing escaped fragment %q in %q", want, line)
		}
	}
	for _, bad := range []string{"a\nb", "a\rb", "a\x00b"} {
		if _, err := processBatchCommandLine(path, []string{bad}); err == nil {
			t.Errorf("accepted an argument with a command delimiter: %q", bad)
		}
	}
}
