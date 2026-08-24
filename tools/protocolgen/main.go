// protocolgen 使用固定工具链把 Codex app-server JSON Schema 生成为 Go 协议快照。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const generationTimeout = 2 * time.Minute

const (
	quicktypeVersionLine = "quicktype version 26.0.0"
	codexVersionLine     = "codex-cli 0.148.0"
)

var errGeneratedFileStale = errors.New("generated protocol snapshot is stale")

// generatorConfig 保存一次协议生成所需的显式路径，避免依赖进程级全局状态。
type generatorConfig struct {
	// repoRoot 是当前 Git worktree 的根目录。
	repoRoot string
	// schemaPath 是已提交的 Codex 默认稳定 schema bundle。
	schemaPath string
	// entryPath 是声明 V1 具名类型入口的 JSON Schema。
	entryPath string
	// outputPath 是提交到 protocol package 的 Go 快照路径。
	outputPath string
	// toolsPath 是带精确 lockfile 的 Node 工具目录。
	toolsPath string
}

// commandOptions 保存命令行选择；生成、检查和刷新 schema 共用同一组路径覆盖参数。
type commandOptions struct {
	// check 表示只比较快照，不修改输出文件。
	check bool
	// refreshSchema 表示先用固定 Codex CLI 重新获取默认稳定 schema。
	refreshSchema bool
	// schemaPath 覆盖默认 schema 输入路径。
	schemaPath string
	// entryPath 覆盖默认 V1 root schema 路径。
	entryPath string
	// outputPath 覆盖默认 Go 快照路径。
	outputPath string
}

// schemaBundle 只读取稳定性校验需要的 definitions 索引，不复制上游完整 schema 模型。
type schemaBundle struct {
	// Definitions 是 bundle 顶层及其 v2 命名空间中的协议定义。
	Definitions map[string]json.RawMessage `json:"definitions"`
}

// main 解析命令并在有界时间内完成 schema 刷新、代码生成或 freshness 检查。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), generationTimeout)
	defer cancel()

	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "protocol generation failed: %v\n", err)
		os.Exit(1)
	}
}

// run 从当前目录定位 worktree，并执行调用者选择的单一生成动作。
func run(ctx context.Context, args []string) error {
	repoRoot, err := findRepositoryRoot()
	if err != nil {
		return err
	}

	options, err := parseArguments(args)
	if err != nil {
		return err
	}
	cfg := applyOptions(defaultConfig(repoRoot), options)

	if options.refreshSchema {
		if err := refreshStableSchema(ctx, cfg); err != nil {
			return err
		}
	}
	if options.check {
		return checkFreshness(ctx, cfg)
	}

	generated, err := generate(ctx, cfg)
	if err != nil {
		return err
	}
	return writeFileAtomically(cfg.outputPath, generated, 0o644)
}

// parseArguments 使用标准库 flag 解析受控命令，未知参数直接返回可诊断错误。
func parseArguments(args []string) (commandOptions, error) {
	var options commandOptions
	flags := flag.NewFlagSet("protocolgen", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.BoolVar(&options.check, "check", false, "check whether generated protocol code is fresh")
	flags.BoolVar(
		&options.refreshSchema,
		"refresh-schema",
		false,
		"refresh the committed schema with Codex 0.148.0 before generation",
	)
	flags.StringVar(&options.schemaPath, "schema", "", "override the committed schema path")
	flags.StringVar(&options.entryPath, "entry", "", "override the protocol root schema path")
	flags.StringVar(&options.outputPath, "output", "", "override the generated Go output path")

	if err := flags.Parse(args); err != nil {
		return commandOptions{}, fmt.Errorf("parsing arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected positional arguments: %q", flags.Args())
	}
	return options, nil
}

// defaultConfig 返回仓库内固定输入、输出和工具 lockfile 的规范位置。
func defaultConfig(repoRoot string) generatorConfig {
	protocolPath := filepath.Join(repoRoot, "agents", "codex", "protocol")
	return generatorConfig{
		repoRoot:   repoRoot,
		schemaPath: filepath.Join(protocolPath, "schema", "codex_app_server_protocol.schemas.json"),
		entryPath:  filepath.Join(protocolPath, "schema", "protocol.root.json"),
		outputPath: filepath.Join(protocolPath, "generated_protocol.go"),
		toolsPath:  filepath.Join(repoRoot, "tools", "protocol"),
	}
}

// applyOptions 仅覆盖调用者显式传入的路径，未指定项继续使用仓库规范位置。
func applyOptions(cfg generatorConfig, options commandOptions) generatorConfig {
	if options.schemaPath != "" {
		cfg.schemaPath = options.schemaPath
	}
	if options.entryPath != "" {
		cfg.entryPath = options.entryPath
	}
	if options.outputPath != "" {
		cfg.outputPath = options.outputPath
	}
	return cfg
}

// generate 调用固定 quicktype，并用 go/format 规范化输出以保证逐字节稳定。
func generate(ctx context.Context, cfg generatorConfig) ([]byte, error) {
	// 先校验固定输入边界，再启动外部工具，避免实验 schema 产生任何候选输出。
	if err := validateStableSchema(cfg.schemaPath); err != nil {
		return nil, err
	}
	if err := ensureTools(ctx, cfg); err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "acp-go-protocolgen-")
	if err != nil {
		return nil, fmt.Errorf("creating generation directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	tempOutput := filepath.Join(tempDir, "generated_protocol.go")
	quicktypePath := filepath.Join(cfg.toolsPath, "node_modules", ".bin", "quicktype")
	// 参数顺序固定，且只传已提交的 root schema；quicktype 不接收目录或动态 glob。
	arguments := []string{
		"--src-lang", "schema",
		"--lang", "go",
		"--package", "protocol",
		"--top-level", "Protocol",
		"--field-tags", "json",
		"--omit-empty",
		"--quiet",
		"--out", tempOutput,
		cfg.entryPath,
	}
	if err := execute(ctx, cfg.repoRoot, quicktypePath, arguments...); err != nil {
		return nil, fmt.Errorf("running quicktype 26.0.0: %w", err)
	}

	raw, err := os.ReadFile(tempOutput)
	if err != nil {
		return nil, fmt.Errorf("reading generated protocol: %w", err)
	}
	formatted, err := format.Source(raw)
	if err != nil {
		return nil, fmt.Errorf("formatting generated protocol: %w", err)
	}
	formatted, err = preserveOptionalNullableFields(formatted)
	if err != nil {
		return nil, err
	}
	formatted, err = restrictExperimentalV1Variants(formatted)
	if err != nil {
		return nil, err
	}
	formatted, err = preserveOpenJSONFields(formatted)
	if err != nil {
		return nil, err
	}
	// 薄适配会改变类型长度；必须再次格式化，避免生成快照携带未对齐的机械差异。
	formatted, err = format.Source(formatted)
	if err != nil {
		return nil, fmt.Errorf("formatting adapted protocol: %w", err)
	}
	// 在写入仓库前执行最后的 generated 标记和 experimental-only 哨兵检查。
	if !bytes.HasPrefix(formatted, []byte("// Code generated")) {
		return nil, errors.New("generated protocol is missing the Code generated marker")
	}
	for _, forbidden := range []string{
		"MockExperimentalMethod",
		"EXPERIMENTAL",
		"ExperimentalFeature",
		"ThreadRealtime",
		"AmazonBedrock",
	} {
		if bytes.Contains(formatted, []byte(forbidden)) {
			return nil, fmt.Errorf("generated protocol contains forbidden V1 surface %q", forbidden)
		}
	}
	return formatted, nil
}

// preserveOpenJSONFields 将 V1 DTO 中上游开放 JsonValue 收窄为不丢字节语义的 json.RawMessage。
func preserveOpenJSONFields(generated []byte) ([]byte, error) {
	replacements := []struct {
		// source 是 quicktype 为开放 JSON 字段生成的 interface{} 类型。
		source string
		// replacement 使用 RawMessage 保持数字精度和未知字段。
		replacement string
	}{
		{
			source:      "LogoutAccountResponse                   map[string]interface{}",
			replacement: "LogoutAccountResponse                   map[string]json.RawMessage",
		},
		{
			source:      "TurnInterruptResponse                   map[string]interface{}",
			replacement: "TurnInterruptResponse                   map[string]json.RawMessage",
		},
		{
			source:      "Desktop                         map[string]interface{}",
			replacement: "Desktop                         map[string]json.RawMessage",
		},
		{source: "Config         interface{}", replacement: "Config         json.RawMessage"},
		{
			source:      "Extensions map[string]interface{}",
			replacement: "Extensions map[string]json.RawMessage",
		},
		{source: "Arguments  interface{}", replacement: "Arguments  json.RawMessage"},
		{
			source:      "Results               []interface{}",
			replacement: "Results               []json.RawMessage",
		},
		{source: "Meta              interface{}", replacement: "Meta              json.RawMessage"},
		{
			source:      "Content           []interface{}",
			replacement: "Content           []json.RawMessage",
		},
		{
			source:      "StructuredContent interface{}",
			replacement: "StructuredContent json.RawMessage",
		},
		{
			source:      "Config                map[string]interface{} `json:\"config,omitempty\"`",
			replacement: "Config                map[string]json.RawMessage `json:\"config,omitempty\"`",
		},
		{
			source:      "Config                map[string]interface{}  `json:\"config,omitempty\"`",
			replacement: "Config                map[string]json.RawMessage  `json:\"config,omitempty\"`",
		},
		{
			source:      "OutputSchema interface{}",
			replacement: "OutputSchema json.RawMessage",
		},
		{
			source:      "Config  Config                 `json:\"config\"`",
			replacement: "Config  json.RawMessage       `json:\"config\"`",
		},
		{
			source:      "Meta            interface{} `json:\"_meta,omitempty\"`",
			replacement: "Meta            json.RawMessage `json:\"_meta,omitempty\"`",
		},
		{
			source:      "RequestedSchema interface{} `json:\"requestedSchema,omitempty\"`",
			replacement: "RequestedSchema json.RawMessage `json:\"requestedSchema,omitempty\"`",
		},
		{
			source:      "Meta   interface{}                `json:\"_meta,omitempty\"`",
			replacement: "Meta   json.RawMessage            `json:\"_meta,omitempty\"`",
		},
		{
			source:      "Content interface{} `json:\"content,omitempty\"`",
			replacement: "Content json.RawMessage `json:\"content,omitempty\"`",
		},
	}

	result := generated
	for _, replacement := range replacements {
		oldValue := []byte(replacement.source)
		if count := bytes.Count(result, oldValue); count != 1 {
			return nil, fmt.Errorf(
				"open JSON source field %q occurred %d times, want exactly once",
				replacement.source,
				count,
			)
		}
		result = bytes.Replace(result, oldValue, []byte(replacement.replacement), 1)
	}
	return result, nil
}

// restrictExperimentalV1Variants 隐藏 V1 不支持的 plan item 构造常量，同时保持未知字符串可前向解码。
func restrictExperimentalV1Variants(generated []byte) ([]byte, error) {
	replacements := []struct {
		// source 是固定 quicktype 输出中的一个实验 surface 片段。
		source string
		// replacement 保留同一 DTO 上的稳定说明或删除实验构造常量。
		replacement string
	}{
		{
			source: "// EXPERIMENTAL - proposed plan item content. The completed plan item is authoritative and\n" +
				"// may not match the concatenation of `PlanDelta` text.\n//\n",
			replacement: "",
		},
		{
			source:      "\tPlan                ThreadItemType = \"plan\"\n",
			replacement: "",
		},
		{
			source:      "//\n// [UNSTABLE] Managed Amazon Bedrock login is experimental.\n",
			replacement: "",
		},
		{
			source:      "\tRegion          *string `json:\"region,omitempty\"`\n",
			replacement: "",
		},
		{
			source:      "\tAccountTypeAmazonBedrock AccountType = \"amazonBedrock\"\n",
			replacement: "",
		},
		{
			source:      "\tTypeAmazonBedrock Type = \"amazonBedrock\"\n",
			replacement: "",
		},
		// item/tool/requestUserInput 是 V1 明确采用的唯一 experimental 请求面；
		// 只移除其上游状态标签，最终哨兵仍会拒绝任何其他 EXPERIMENTAL surface。
		{
			source:      "// EXPERIMENTAL. Captures a user's answer to a request_user_input question.\n",
			replacement: "// Captures a user's answer to a request_user_input question.\n",
		},
		{
			source:      "// EXPERIMENTAL. Defines a single selectable option for request_user_input.\n",
			replacement: "// Defines a single selectable option for request_user_input.\n",
		},
		{
			source:      "// EXPERIMENTAL. Params sent with a request_user_input event.\n",
			replacement: "// Params sent with a request_user_input event.\n",
		},
		{
			source:      "// EXPERIMENTAL. Represents one request_user_input question and its required options.\n",
			replacement: "// Represents one request_user_input question and its required options.\n",
		},
		{
			source:      "// EXPERIMENTAL. Response payload mapping question ids to answers.\n",
			replacement: "// Response payload mapping question ids to answers.\n",
		},
		// 新增 elicitation 枚举后 quicktype 会为既有 V1 常量改名；恢复原公开标识，
		// 新 MCP 枚举本身已带类型前缀，不会产生 Go 标识冲突。
		{
			source:      "\tFileChangeApprovalDecisionAccept  FileChangeApprovalDecision = \"accept\"\n",
			replacement: "\tAccept                              FileChangeApprovalDecision = \"accept\"\n",
		},
		{
			source:      "\tFileChangeApprovalDecisionCancel  FileChangeApprovalDecision = \"cancel\"\n",
			replacement: "\tCancel                              FileChangeApprovalDecision = \"cancel\"\n",
		},
		{
			source:      "\tFileChangeApprovalDecisionDecline FileChangeApprovalDecision = \"decline\"\n",
			replacement: "\tDecline                             FileChangeApprovalDecision = \"decline\"\n",
		},
		{
			source:      "\tFluffyFailed      TurnStatus = \"failed\"\n",
			replacement: "\tFailed            TurnStatus = \"failed\"\n",
		},
	}

	result := generated
	for _, replacement := range replacements {
		oldValue := []byte(replacement.source)
		if count := bytes.Count(result, oldValue); count != 1 {
			return nil, fmt.Errorf(
				"experimental V1 source fragment occurred %d times, want exactly once",
				count,
			)
		}
		result = bytes.Replace(result, oldValue, []byte(replacement.replacement), 1)
	}
	return result, nil
}

// preserveOptionalNullableFields 对 quicktype 无法表达的两个 V1 三态字段做最薄的确定性适配。
func preserveOptionalNullableFields(generated []byte) ([]byte, error) {
	replacements := []struct {
		// field 是 quicktype 对 optional+nullable 的固定退化输出。
		field string
		// replacement 使用支持 absent/null/value 的仓库内通用值类型。
		replacement string
	}{
		{
			field:       "GrantRoot *string `json:\"grantRoot,omitempty\"`",
			replacement: "GrantRoot OptionalNullable[string] `json:\"grantRoot,omitzero\"`",
		},
		{
			field:       "StrictAutoReview *bool `json:\"strictAutoReview,omitempty\"`",
			replacement: "StrictAutoReview OptionalNullable[bool] `json:\"strictAutoReview,omitzero\"`",
		},
	}

	result := generated
	for _, replacement := range replacements {
		oldValue := []byte(replacement.field)
		if count := bytes.Count(result, oldValue); count != 1 {
			return nil, fmt.Errorf(
				"optional+nullable source field %q occurred %d times, want exactly once",
				replacement.field,
				count,
			)
		}
		result = bytes.Replace(result, oldValue, []byte(replacement.replacement), 1)
	}
	return result, nil
}

// checkFreshness 重新生成到内存，并与目标文件逐字节比较而不修改工作树。
func checkFreshness(ctx context.Context, cfg generatorConfig) error {
	generated, err := generate(ctx, cfg)
	if err != nil {
		return err
	}
	committed, err := os.ReadFile(cfg.outputPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errGeneratedFileStale
		}
		return fmt.Errorf("reading generated snapshot: %w", err)
	}
	if !bytes.Equal(generated, committed) {
		return errGeneratedFileStale
	}
	return nil
}

// refreshStableSchema 使用 lockfile 中的 Codex 0.148.0，且刻意不传 --experimental。
func refreshStableSchema(ctx context.Context, cfg generatorConfig) error {
	if err := ensureTools(ctx, cfg); err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp("", "acp-go-codex-schema-")
	if err != nil {
		return fmt.Errorf("creating schema directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	codexPath := filepath.Join(cfg.toolsPath, "node_modules", ".bin", "codex")
	// 此处命令参数是稳定 API 边界：不得加入 --experimental。
	if err := execute(
		ctx,
		cfg.repoRoot,
		codexPath,
		"app-server",
		"generate-json-schema",
		"--out",
		tempDir,
	); err != nil {
		return fmt.Errorf("generating Codex 0.148.0 stable schema: %w", err)
	}

	generatedSchema := filepath.Join(tempDir, "codex_app_server_protocol.schemas.json")
	raw, err := os.ReadFile(generatedSchema)
	if err != nil {
		return fmt.Errorf("reading refreshed stable schema: %w", err)
	}
	if err := validateStableSchemaBytes(raw); err != nil {
		return err
	}
	return writeFileAtomically(cfg.schemaPath, raw, 0o644)
}

// validateStableSchema 读取并校验固定 bundle，阻止 experimental-only 定义进入生成链路。
func validateStableSchema(schemaPath string) error {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("reading stable schema: %w", err)
	}
	return validateStableSchemaBytes(raw)
}

// validateStableSchemaBytes 校验 JSON 结构以及 0.148.0 已知的 experimental-only 哨兵类型。
func validateStableSchemaBytes(raw []byte) error {
	var bundle schemaBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return fmt.Errorf("parsing stable schema: %w", err)
	}
	if len(bundle.Definitions) == 0 {
		return errors.New("stable schema has no definitions")
	}

	for _, name := range []string{"CurrentTimeReadParams", "MockExperimentalMethodParams"} {
		exists, err := definitionExists(bundle.Definitions, name)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("stable schema contains experimental-only definition %q", name)
		}
	}
	return nil
}

// definitionExists 同时检查 bundle 顶层和 Codex 使用的 v2 嵌套 definitions 命名空间。
func definitionExists(definitions map[string]json.RawMessage, name string) (bool, error) {
	if _, exists := definitions[name]; exists {
		return true, nil
	}

	rawV2, exists := definitions["v2"]
	if !exists {
		return false, nil
	}
	var v2Definitions map[string]json.RawMessage
	if err := json.Unmarshal(rawV2, &v2Definitions); err != nil {
		return false, fmt.Errorf("parsing v2 schema definitions: %w", err)
	}
	_, exists = v2Definitions[name]
	return exists, nil
}

// ensureTools 通过 package-lock 安装精确版本工具；仅复用版本输出完全匹配的现有工具集。
func ensureTools(ctx context.Context, cfg generatorConfig) error {
	matched, _ := toolsMatchLockedVersions(ctx, cfg)
	if matched {
		return nil
	}

	if err := execute(
		ctx,
		cfg.repoRoot,
		"npm",
		"ci",
		"--ignore-scripts",
		"--prefix",
		cfg.toolsPath,
	); err != nil {
		return fmt.Errorf("installing locked protocol tools: %w", err)
	}
	matched, err := toolsMatchLockedVersions(ctx, cfg)
	if err != nil {
		return fmt.Errorf("checking protocol tool versions after npm ci: %w", err)
	}
	if !matched {
		return errors.New("locked protocol tool versions do not match after npm ci")
	}
	return nil
}

// toolsMatchLockedVersions 同时校验工具存在性和 `--version` 首行，避免陈旧 node_modules 绕过 lockfile。
func toolsMatchLockedVersions(ctx context.Context, cfg generatorConfig) (bool, error) {
	checks := []struct {
		// path 是 lockfile 安装后的本地可执行文件。
		path string
		// expected 是该固定版本的规范版本输出首行。
		expected string
	}{
		{
			path:     filepath.Join(cfg.toolsPath, "node_modules", ".bin", "quicktype"),
			expected: quicktypeVersionLine,
		},
		{
			path:     filepath.Join(cfg.toolsPath, "node_modules", ".bin", "codex"),
			expected: codexVersionLine,
		},
	}

	for _, check := range checks {
		if !isExecutable(check.path) {
			return false, nil
		}
		output, err := executeOutput(ctx, cfg.repoRoot, check.path, "--version")
		if err != nil {
			return false, err
		}
		firstLine, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
		if firstLine != check.expected {
			return false, nil
		}
	}
	return true, nil
}

// isExecutable 判断生成工具路径是否存在且不是目录。
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// execute 在指定目录运行外部工具，并把失败输出包装进单一错误链供上层处理。
func execute(ctx context.Context, directory string, name string, arguments ...string) error {
	_, err := executeOutput(ctx, directory, name, arguments...)
	return err
}

// executeOutput 在指定目录运行外部工具，并保留成功输出供版本等只读校验使用。
func executeOutput(ctx context.Context, directory string, name string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, message)
	}
	return string(output), nil
}

// writeFileAtomically 先在目标目录完整写入临时文件，再替换目标，避免留下半成品快照。
func writeFileAtomically(path string, data []byte, mode os.FileMode) (resultErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	tempFile, err := os.CreateTemp(directory, ".protocolgen-*")
	if err != nil {
		return fmt.Errorf("creating temporary output: %w", err)
	}
	tempPath := tempFile.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := tempFile.Close(); resultErr == nil && closeErr != nil {
				resultErr = fmt.Errorf("closing temporary output: %w", closeErr)
			}
		}
		_ = os.Remove(tempPath)
	}()

	if err := tempFile.Chmod(mode); err != nil {
		return fmt.Errorf("setting temporary output mode: %w", err)
	}
	if _, err := tempFile.Write(data); err != nil {
		return fmt.Errorf("writing temporary output: %w", err)
	}
	// rename 前必须显式关闭写句柄，保证目标只会看到完整文件。
	closeErr := tempFile.Close()
	closed = true
	if closeErr != nil {
		return fmt.Errorf("closing temporary output before replace: %w", closeErr)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replacing generated output: %w", err)
	}
	return nil
}

// findRepositoryRoot 从当前目录向上寻找 go.mod，允许 go generate 从 package 子目录执行。
func findRepositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("reading current directory: %w", err)
	}

	for {
		// go.mod 是所有 worktree 都具备且不会被生成器创建的稳定仓库标记。
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("checking repository marker: %w", err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("go.mod not found above current directory")
		}
		current = parent
	}
}
