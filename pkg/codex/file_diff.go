package codex

import (
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/codex/protocol"

	acp "github.com/coder/acp-go-sdk"
)

var (
	// errInvalidUnifiedPatch 表示 Codex update diff 不能安全应用到当前文件内容。
	errInvalidUnifiedPatch = errors.New("invalid Codex unified patch")
	// errDiffFileTooLarge 表示完整文件差异超出可安全缓存的大小。
	errDiffFileTooLarge = errors.New("Codex diff file exceeds 8 MiB")
	// unifiedHunkHeaderPattern 解析 unified diff 的标准 hunk 区间。
	unifiedHunkHeaderPattern = regexp.MustCompile(
		`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`,
	)
)

// unifiedPatch 保存已经校验过的 unified diff hunk。
type unifiedPatch struct {
	// Hunks 是补丁中按源文件顺序排列的变更区间。
	Hunks []unifiedPatchHunk
}

// unifiedPatchHunk 保存单个 unified diff 区间及其原始行操作。
type unifiedPatchHunk struct {
	// OldStart 是旧文件中 1-based 的区间起始行。
	OldStart int
	// OldLines 是旧文件参与该区间的行数。
	OldLines int
	// NewStart 是新文件中 1-based 的区间起始行。
	NewStart int
	// NewLines 是新文件参与该区间的行数。
	NewLines int
	// Lines 是带上下文、删除和新增语义的补丁行。
	Lines []unifiedPatchLine
}

// unifiedPatchLine 保存一行补丁操作及不含操作前缀的原始文本。
type unifiedPatchLine struct {
	// Kind 是空格、减号或加号三种 unified diff 行前缀。
	Kind byte
	// Text 保留该行的换行符；no-newline 标记会移除末尾换行。
	Text string
}

// createFileDiffContent 把 fileChange 转换为标准 ACP diff。
func createFileDiffContent(change protocol.ChangeElement) (acp.ToolCallContent, bool) {
	switch change.Kind.Type {
	case protocol.Add:
		diff := acp.ToolDiffContent(change.Path, change.Diff)
		diff.Diff.Meta = map[string]any{"kind": "add"}
		return diff, true
	case protocol.Delete:
		diff := acp.ToolDiffContent(change.Path, "", change.Diff)
		diff.Diff.Meta = map[string]any{"kind": "delete"}
		return diff, true
	case protocol.Update:
		return createUpdatedFileDiffContent(change)
	default:
		return acp.ToolCallContent{}, false
	}
}

// createUpdatedFileDiffContent 通过应用或反向应用补丁还原完整的新旧文件内容。
func createUpdatedFileDiffContent(change protocol.ChangeElement) (acp.ToolCallContent, bool) {
	patch, err := parseUnifiedPatch(recoverCodexUnifiedDiff(change.Diff))
	if err != nil {
		return acp.ToolCallContent{}, false
	}

	oldContent, err := readDiffFile(change.Path)
	if err == nil {
		current := string(oldContent)
		if patched, applyErr := applyUnifiedPatch(current, patch, false); applyErr == nil {
			path := change.Path
			if change.Kind.MovePath != nil && *change.Kind.MovePath != "" {
				path = *change.Kind.MovePath
			}
			return updateDiffContent(path, current, patched), true
		}
		// Full-access 模式下 started 事件到达时文件可能已经修改，反向应用用于恢复旧内容。
		if reverted, revertErr := applyUnifiedPatch(current, patch, true); revertErr == nil {
			return updateDiffContent(change.Path, reverted, current), true
		}
		return acp.ToolCallContent{}, false
	}

	if change.Kind.MovePath == nil || *change.Kind.MovePath == "" {
		return acp.ToolCallContent{}, false
	}
	newContent, err := readDiffFile(*change.Kind.MovePath)
	if err != nil {
		return acp.ToolCallContent{}, false
	}
	current := string(newContent)
	reverted, err := applyUnifiedPatch(current, patch, true)
	if err != nil {
		return acp.ToolCallContent{}, false
	}
	return updateDiffContent(*change.Kind.MovePath, reverted, current), true
}

// readDiffFile 限制补丁还原前读取的文件大小，超限时退回普通工具内容。
func readDiffFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const maxDiffFileBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxDiffFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDiffFileBytes {
		return nil, errDiffFileTooLarge
	}
	return data, nil
}

// updateDiffContent 创建携带 update 元数据的标准 ACP diff 内容。
func updateDiffContent(path, oldText, newText string) acp.ToolCallContent {
	diff := acp.ToolDiffContent(path, newText, oldText)
	diff.Diff.Meta = map[string]any{"kind": "update"}
	return diff
}

// recoverCodexUnifiedDiff 移除 Codex 追加在补丁尾部的移动目标说明。
func recoverCodexUnifiedDiff(diff string) string {
	marker := "\n\nMoved to: "
	if index := strings.LastIndex(diff, marker); index >= 0 {
		return diff[:index]
	}
	return diff
}

// parseUnifiedPatch 解析并校验 unified diff hunk，不接受无法证明安全的松散文本。
func parseUnifiedPatch(diff string) (unifiedPatch, error) {
	var patch unifiedPatch
	var current *unifiedPatchHunk
	for _, line := range splitLinesPreservingEndings(diff) {
		if matches := unifiedHunkHeaderPattern.FindStringSubmatch(strings.TrimSuffix(line, "\n")); matches != nil {
			hunk, err := unifiedPatchHunkFromHeader(matches)
			if err != nil {
				return unifiedPatch{}, err
			}
			patch.Hunks = append(patch.Hunks, hunk)
			current = &patch.Hunks[len(patch.Hunks)-1]
			continue
		}
		if current == nil {
			// 文件标题属于合法 unified diff；没有 hunk 的内容最终仍会被拒绝。
			continue
		}
		if strings.HasPrefix(line, `\ No newline at end of file`) {
			if len(current.Lines) == 0 {
				return unifiedPatch{}, errInvalidUnifiedPatch
			}
			last := &current.Lines[len(current.Lines)-1]
			last.Text = strings.TrimSuffix(last.Text, "\n")
			continue
		}
		if line == "" {
			continue
		}
		switch line[0] {
		case ' ', '-', '+':
			current.Lines = append(current.Lines, unifiedPatchLine{Kind: line[0], Text: line[1:]})
		default:
			return unifiedPatch{}, errInvalidUnifiedPatch
		}
	}
	if len(patch.Hunks) == 0 {
		return unifiedPatch{}, errInvalidUnifiedPatch
	}
	for _, hunk := range patch.Hunks {
		oldLines, newLines := 0, 0
		for _, line := range hunk.Lines {
			if line.Kind != '+' {
				oldLines++
			}
			if line.Kind != '-' {
				newLines++
			}
		}
		if oldLines != hunk.OldLines || newLines != hunk.NewLines {
			return unifiedPatch{}, errInvalidUnifiedPatch
		}
	}
	return patch, nil
}

// unifiedPatchHunkFromHeader 把正则捕获的区间转换为整数 hunk。
func unifiedPatchHunkFromHeader(matches []string) (unifiedPatchHunk, error) {
	oldStart, err := strconv.Atoi(matches[1])
	if err != nil {
		return unifiedPatchHunk{}, errInvalidUnifiedPatch
	}
	oldLines, err := unifiedRangeCount(matches[2])
	if err != nil {
		return unifiedPatchHunk{}, err
	}
	newStart, err := strconv.Atoi(matches[3])
	if err != nil {
		return unifiedPatchHunk{}, errInvalidUnifiedPatch
	}
	newLines, err := unifiedRangeCount(matches[4])
	if err != nil {
		return unifiedPatchHunk{}, err
	}
	return unifiedPatchHunk{
		OldStart: oldStart,
		OldLines: oldLines,
		NewStart: newStart,
		NewLines: newLines,
	}, nil
}

// unifiedRangeCount 返回 hunk 省略行数时的标准默认值 1。
func unifiedRangeCount(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errInvalidUnifiedPatch
	}
	return value, nil
}

// applyUnifiedPatch 把补丁正向或反向应用到完整文本，任何上下文不匹配都失败。
func applyUnifiedPatch(content string, patch unifiedPatch, reverse bool) (string, error) {
	source := splitLinesPreservingEndings(content)
	output := make([]string, 0, len(source))
	cursor := 0
	for _, hunk := range patch.Hunks {
		start, count := hunk.OldStart, hunk.OldLines
		if reverse {
			start, count = hunk.NewStart, hunk.NewLines
		}
		position := unifiedRangePosition(start, count)
		if position < cursor || position > len(source) {
			return "", errInvalidUnifiedPatch
		}
		output = append(output, source[cursor:position]...)
		cursor = position
		for _, line := range hunk.Lines {
			kind := line.Kind
			if reverse {
				switch kind {
				case '-':
					kind = '+'
				case '+':
					kind = '-'
				}
			}
			switch kind {
			case ' ':
				if cursor >= len(source) || source[cursor] != line.Text {
					return "", errInvalidUnifiedPatch
				}
				output = append(output, source[cursor])
				cursor++
			case '-':
				if cursor >= len(source) || source[cursor] != line.Text {
					return "", errInvalidUnifiedPatch
				}
				cursor++
			case '+':
				output = append(output, line.Text)
			default:
				return "", errInvalidUnifiedPatch
			}
		}
	}
	output = append(output, source[cursor:]...)
	return strings.Join(output, ""), nil
}

// unifiedRangePosition 把 unified diff 的 1-based 区间转换为文本行切片下标。
func unifiedRangePosition(start, count int) int {
	if count == 0 {
		return start
	}
	return start - 1
}

// splitLinesPreservingEndings 拆分文本并把换行符保留在对应行上。
func splitLinesPreservingEndings(value string) []string {
	if value == "" {
		return nil
	}
	lines := strings.SplitAfter(value, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}
