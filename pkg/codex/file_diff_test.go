package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"
)

// TestCreateUpdatedFileDiffContentMatchesCodexUpstream 验证未应用和已应用补丁都能还原完整内容。
func TestCreateUpdatedFileDiffContentMatchesCodexUpstream(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		// name 是当前文件所处阶段。
		name string
		// current 是收到 started 事件时磁盘上的文件内容。
		current string
	}{
		{name: "not_applied", current: "package test\n\nclass OldFile {}\n"},
		{name: "already_applied", current: "package test\n\nclass UpdatedFile {}\n"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "File.kt")
			if err := os.WriteFile(path, []byte(test.current), 0o600); err != nil {
				t.Fatal(err)
			}
			content, ok := createFileDiffContent(protocol.ChangeElement{
				Path: path,
				Kind: protocol.PatchChangeKind{Type: protocol.Update},
				Diff: "@@ -1,3 +1,3 @@\n package test\n \n-class OldFile {}\n+class UpdatedFile {}\n",
			})
			if !ok || content.Diff == nil || content.Diff.OldText == nil {
				t.Fatalf("diff content = %#v", content)
			}
			if got, want := *content.Diff.OldText, "package test\n\nclass OldFile {}\n"; got != want {
				t.Fatalf("oldText = %q，期望 %q", got, want)
			}
			if got, want := content.Diff.NewText, "package test\n\nclass UpdatedFile {}\n"; got != want {
				t.Fatalf("newText = %q，期望 %q", got, want)
			}
		})
	}
}

// TestCreateUpdatedFileDiffContentHandlesMovedFile 验证文件已经移动后可从目标文件反向恢复旧内容。
func TestCreateUpdatedFileDiffContentHandlesMovedFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	oldPath := filepath.Join(directory, "Old.txt")
	newPath := filepath.Join(directory, "New.txt")
	if err := os.WriteFile(newPath, []byte("new code line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, ok := createFileDiffContent(protocol.ChangeElement{
		Path: oldPath,
		Kind: protocol.PatchChangeKind{Type: protocol.Update, MovePath: &newPath},
		Diff: "@@ -1 +1 @@\n-old code line\n+new code line\n\n\nMoved to: " + newPath,
	})
	if !ok || content.Diff == nil || content.Diff.OldText == nil ||
		content.Diff.Path != newPath || *content.Diff.OldText != "old code line\n" ||
		content.Diff.NewText != "new code line\n" {
		t.Fatalf("moved diff = %#v", content)
	}
}

// TestApplyUnifiedPatchPreservesMissingFinalNewline 验证 no-newline 标记不会伪造文件尾换行。
func TestApplyUnifiedPatchPreservesMissingFinalNewline(t *testing.T) {
	t.Parallel()

	patch, err := parseUnifiedPatch("@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := applyUnifiedPatch("old", patch, false)
	if err != nil || updated != "new" {
		t.Fatalf("updated = %q, error = %v", updated, err)
	}
}
