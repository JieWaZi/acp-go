package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const stableSchemaSHA256 = "819fe7b47288cc74da5190743390c8d1faef403f5401a1868b306dac195b1944"

// TestGenerateIsByteDeterministic 防止生成器把时间、临时路径或非稳定遍历顺序写入协议快照。
func TestGenerateIsByteDeterministic(t *testing.T) {
	cfg := defaultConfig(testRepositoryRoot(t))

	first, err := generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("第一次生成协议代码失败：%v", err)
	}
	second, err := generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("第二次生成协议代码失败：%v", err)
	}

	if string(first) != string(second) {
		t.Fatal("相同 schema 连续生成的结果不一致")
	}
}

// TestGenerateMarksV1RootDescription 防止 generated 文件失去禁止手改标记或 V1 根说明。
func TestGenerateMarksV1RootDescription(t *testing.T) {
	cfg := defaultConfig(testRepositoryRoot(t))

	generated, err := generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("生成协议代码失败：%v", err)
	}
	text := string(generated)

	if !strings.HasPrefix(text, "// Code generated") {
		t.Fatal("生成文件缺少标准 Code generated 标记")
	}
	if !strings.Contains(text, "V1 Codex app-server protocol roots used by the runtime") {
		t.Fatal("生成文件缺少 V1 协议根说明")
	}
	if strings.Contains(text, "MockExperimentalMethod") {
		t.Fatal("默认稳定生成结果意外包含 experimental-only 类型")
	}
}

// TestGenerateExcludesExperimentalPublicSurface 防止完整 envelope 把实验方法和类型带入 V1 包。
func TestGenerateExcludesExperimentalPublicSurface(t *testing.T) {
	cfg := defaultConfig(testRepositoryRoot(t))

	generated, err := generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("生成协议代码失败：%v", err)
	}
	text := string(generated)

	for _, forbidden := range []string{
		"EXPERIMENTAL",
		"ExperimentalFeature",
		"ThreadRealtime",
		"type AppListUpdatedNotification",
		"type PlanDeltaNotification",
		"AmazonBedrock",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("V1 生成结果包含实验 surface %q", forbidden)
		}
	}
}

// TestGeneratePreservesOptionalNullableFields 防止 optional+nullable 被指针压成无法区分缺省和 null 的二态值。
func TestGeneratePreservesOptionalNullableFields(t *testing.T) {
	cfg := defaultConfig(testRepositoryRoot(t))

	generated, err := generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("生成协议代码失败：%v", err)
	}
	text := string(generated)

	for _, expected := range []string{
		"GrantRoot OptionalNullable[string] `json:\"grantRoot,omitzero\"`",
		"StrictAutoReview OptionalNullable[bool] `json:\"strictAutoReview,omitzero\"`",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("生成结果缺少 optional+nullable 适配 %q", expected)
		}
	}
}

// TestGenerateAvoidsOpaqueCoreDTOFields 防止生成 DTO 字段重新退化为 interface{}。
func TestGenerateAvoidsOpaqueCoreDTOFields(t *testing.T) {
	cfg := defaultConfig(testRepositoryRoot(t))

	generated, err := generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("生成协议代码失败：%v", err)
	}
	text := string(generated)

	for _, forbidden := range []string{
		"interface{} `json:",
		"]interface{} `json:",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("生成 DTO 仍包含不透明字段 %q", forbidden)
		}
	}
}

// TestCheckFreshnessRejectsModifiedOutput 防止 freshness 检查把被手改的生成快照误判为最新。
func TestCheckFreshnessRejectsModifiedOutput(t *testing.T) {
	cfg := defaultConfig(testRepositoryRoot(t))
	generated, err := generate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("生成协议代码失败：%v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "generated_protocol.go")
	if err := os.WriteFile(outputPath, append(generated, []byte("// stale\n")...), 0o600); err != nil {
		t.Fatalf("写入过期快照失败：%v", err)
	}
	cfg.outputPath = outputPath

	err = checkFreshness(context.Background(), cfg)
	if !errors.Is(err, errGeneratedFileStale) {
		t.Fatalf("过期快照错误 = %v，期望 errors.Is(err, errGeneratedFileStale)", err)
	}
}

// TestSchemaIsPinnedStableSurface 防止固定输入被静默替换，或混入只有 --experimental 才生成的定义。
func TestSchemaIsPinnedStableSurface(t *testing.T) {
	cfg := defaultConfig(testRepositoryRoot(t))
	raw, err := os.ReadFile(cfg.schemaPath)
	if err != nil {
		t.Fatalf("读取固定 schema 失败：%v", err)
	}

	digest := sha256.Sum256(raw)
	if got := hex.EncodeToString(digest[:]); got != stableSchemaSHA256 {
		t.Fatalf("schema SHA-256 = %s，期望 %s", got, stableSchemaSHA256)
	}

	var schema struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("解析固定 schema 失败：%v", err)
	}
	for _, name := range []string{"CurrentTimeReadParams", "MockExperimentalMethodParams"} {
		if _, exists := schema.Definitions[name]; exists {
			t.Fatalf("固定稳定 schema 意外包含 experimental-only 定义 %q", name)
		}
	}
}

// TestValidateStableSchemaRejectsNestedExperimentalDefinition 防止 v2 命名空间绕过稳定 surface 校验。
func TestValidateStableSchemaRejectsNestedExperimentalDefinition(t *testing.T) {
	raw := []byte(`{"definitions":{"v2":{"MockExperimentalMethodParams":{}}}}`)

	err := validateStableSchemaBytes(raw)
	if err == nil || !strings.Contains(err.Error(), "MockExperimentalMethodParams") {
		t.Fatalf("嵌套 experimental 定义错误 = %v，期望明确拒绝", err)
	}
}

// TestToolsMatchLockedVersionsRejectsStaleInstall 防止仅凭可执行文件存在就复用错误版本。
func TestToolsMatchLockedVersionsRejectsStaleInstall(t *testing.T) {
	toolsPath := t.TempDir()
	binPath := filepath.Join(toolsPath, "node_modules", ".bin")
	if err := os.MkdirAll(binPath, 0o755); err != nil {
		t.Fatalf("创建伪工具目录失败：%v", err)
	}
	writeFakeTool := func(name string, output string) {
		t.Helper()
		path := filepath.Join(binPath, name)
		content := "#!/bin/sh\nprintf '%s\\n' '" + output + "'\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("写入伪工具 %s 失败：%v", name, err)
		}
	}
	writeFakeTool("quicktype", "quicktype version 25.0.0")
	writeFakeTool("codex", "codex-cli 0.148.0")

	matched, err := toolsMatchLockedVersions(context.Background(), generatorConfig{toolsPath: toolsPath})
	if err != nil {
		t.Fatalf("校验伪工具版本失败：%v", err)
	}
	if matched {
		t.Fatal("错误 quicktype 版本被当作 lockfile 固定版本复用")
	}
}

// TestWriteFileAtomicallyReplacesTarget 防止成功替换后因重复关闭临时文件而返回伪失败。
func TestWriteFileAtomicallyReplacesTarget(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "generated_protocol.go")
	if err := os.WriteFile(outputPath, []byte("old"), 0o600); err != nil {
		t.Fatalf("写入旧快照失败：%v", err)
	}

	if err := writeFileAtomically(outputPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("原子替换快照失败：%v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取新快照失败：%v", err)
	}
	if string(got) != "new" {
		t.Fatalf("快照内容 = %q，期望 %q", got, "new")
	}
}

// testRepositoryRoot 从当前测试文件位置推导 worktree 根目录，避免依赖调用者工作目录。
func testRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试源文件")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
