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
	// 在写入仓库前执行最后的 generated 标记和 experimental-only 哨兵检查。
	if !bytes.HasPrefix(formatted, []byte("// Code generated")) {
		return nil, errors.New("generated protocol is missing the Code generated marker")
	}
	if bytes.Contains(formatted, []byte("MockExperimentalMethod")) {
		return nil, errors.New("generated protocol contains experimental-only definitions")
	}
	return formatted, nil
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

// ensureTools 通过 package-lock 安装精确版本工具；已存在完整工具集时不重复安装。
func ensureTools(ctx context.Context, cfg generatorConfig) error {
	quicktypePath := filepath.Join(cfg.toolsPath, "node_modules", ".bin", "quicktype")
	codexPath := filepath.Join(cfg.toolsPath, "node_modules", ".bin", "codex")
	quicktypeReady := isExecutable(quicktypePath)
	codexReady := isExecutable(codexPath)
	if quicktypeReady && codexReady {
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
	if !isExecutable(quicktypePath) || !isExecutable(codexPath) {
		return errors.New("locked protocol tools are incomplete after npm ci")
	}
	return nil
}

// isExecutable 判断生成工具路径是否存在且不是目录。
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// execute 在指定目录运行外部工具，并把失败输出包装进单一错误链供上层处理。
func execute(ctx context.Context, directory string, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
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
