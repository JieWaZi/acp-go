package nativeacp

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestBatchCommandLineQuotesAndRejectsDelimiters(t *testing.T) {
	line, err := batchCommandLine(`C:\Program Files\tool.cmd`, []string{"--model", `a&b`, `say "hi"`, `C:\work\`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`C:\Program^ Files\tool.cmd`, `^"--model^"`, `^"a^&b^"`, `^"say^ \^"hi\^"^"`, `^"C:\work\\^"`} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing quoted token %q in %q", want, line)
		}
	}
	for _, bad := range []string{"a\nb", "a\rb", "a\x00b"} {
		if _, err := batchCommandLine("tool.cmd", []string{bad}); err == nil {
			t.Fatalf("accepted command delimiter %q", bad)
		}
	}
	shim, err := batchCommandLine(`C:\app\node_modules\.bin\tool.cmd`, []string{"a&b"})
	if err != nil || !strings.Contains(shim, `a^^^&b`) {
		t.Fatalf("shim forwarding pass missing: %q %v", shim, err)
	}
}

func TestCommandContextFreezesEnvironment(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"LAUNCH_TEST=original"}
	args := []string{"--version"}
	command, err := CommandContext(context.Background(), binary, args, environment)
	if err != nil {
		t.Fatal(err)
	}
	environment[0] = "LAUNCH_TEST=changed"
	args[0] = "--changed"
	if command.Env[0] != "LAUNCH_TEST=original" || command.Args[1] != "--version" {
		t.Fatal("launch identity changed through caller buffers")
	}
}
