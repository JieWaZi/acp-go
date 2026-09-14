package nativeacp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNativeACPProductionIsProviderNeutral 防止厂商私有协议重新渗入公共传输层。
func TestNativeACPProductionIsProviderNeutral(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve nativeacp directory")
	}
	directory := filepath.Dir(currentFile)
	forbidden := []string{"codex", "claude", "cursor", "kimi", "pi-acp", "/pkg/pi"}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lowered := strings.ToLower(string(data))
		for _, value := range forbidden {
			if strings.Contains(lowered, value) {
				t.Fatalf("shared native ACP file %s contains provider-specific behavior %q", entry.Name(), value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
