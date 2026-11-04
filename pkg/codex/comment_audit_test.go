package codex

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestHandwrittenGoDeclarationsHaveComments checks meaningful comments on handwritten declarations.
// Generated protocol comments belong to the schema and are identified by the generator marker.
func TestHandwrittenGoDeclarationsHaveComments(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	fileSet := token.NewFileSet()
	var problems []string

	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && shouldSkipCommentAuditDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			// 嵌套 Go 模块独立维护与验证，不属于当前模块的手写代码审计范围。
			if path != repositoryRoot {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				} else if !os.IsNotExist(err) {
					return err
				}
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(content, []byte("Code generated")) {
			return nil
		}
		parsed, parseErr := parser.ParseFile(fileSet, path, content, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		auditHandwrittenFile(fileSet, parsed, &problems)
		return nil
	})
	if err != nil {
		t.Fatalf("扫描手写 Go 注释失败: %v", err)
	}
	if len(problems) == 0 {
		return
	}
	sort.Strings(problems)
	t.Fatalf("以下手写 Go 声明缺少有效注释:\n%s", strings.Join(problems, "\n"))
}

// shouldSkipCommentAuditDirectory 判断不属于本仓库手写 Go 源码的目录。
func shouldSkipCommentAuditDirectory(name string) bool {
	switch name {
	case ".git", ".upstream", ".worktrees", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

// TestCommentAuditDirectoryScope 保证旧工作树和外部依赖不会混入当前分支的注释审计。
func TestCommentAuditDirectoryScope(t *testing.T) {
	for _, name := range []string{".git", ".upstream", ".worktrees", "node_modules", "vendor"} {
		if !shouldSkipCommentAuditDirectory(name) {
			t.Errorf("外部目录 %q 必须跳过注释审计", name)
		}
	}
	for _, name := range []string{"pkg", "internal", "cmd", ".agents"} {
		if shouldSkipCommentAuditDirectory(name) {
			t.Errorf("当前分支目录 %q 不能跳过注释审计", name)
		}
	}
}

// auditHandwrittenFile 检查类型、函数以及结构体和接口字段的声明注释。
func auditHandwrittenFile(fileSet *token.FileSet, file *ast.File, problems *[]string) {
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if !hasDeclarationComment(typed.Doc, typed.Name.Name) {
				appendCommentProblem(fileSet, typed.Pos(), "func "+typed.Name.Name, problems)
			}
		case *ast.GenDecl:
			if typed.Tok != token.TYPE {
				continue
			}
			for _, specification := range typed.Specs {
				typeSpec := specification.(*ast.TypeSpec)
				comment := typeSpec.Doc
				if comment == nil {
					comment = typed.Doc
				}
				if !hasDeclarationComment(comment, typeSpec.Name.Name) {
					appendCommentProblem(fileSet, typeSpec.Pos(), "type "+typeSpec.Name.Name, problems)
				}
			}
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.StructType:
			auditFieldComments(fileSet, typed.Fields, "struct field", problems)
		case *ast.InterfaceType:
			auditFieldComments(fileSet, typed.Methods, "interface method", problems)
		}
		return true
	})
}

// auditFieldComments 检查字段或接口方法的前置/行尾注释是否包含中文说明。
func auditFieldComments(
	fileSet *token.FileSet,
	fields *ast.FieldList,
	kind string,
	problems *[]string,
) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		comment := field.Doc
		if comment == nil {
			comment = field.Comment
		}
		valid := hasComment(comment)
		for _, identifier := range field.Names {
			if ast.IsExported(identifier.Name) && !hasDeclarationComment(comment, identifier.Name) {
				valid = false
			}
		}
		if valid {
			continue
		}
		name := "embedded"
		if len(field.Names) != 0 {
			names := make([]string, 0, len(field.Names))
			for _, identifier := range field.Names {
				names = append(names, identifier.Name)
			}
			name = strings.Join(names, ",")
		}
		appendCommentProblem(fileSet, field.Pos(), kind+" "+name, problems)
	}
}

// hasComment rejects absent and whitespace-only documentation.
func hasComment(group *ast.CommentGroup) bool {
	return group != nil && strings.TrimSpace(group.Text()) != ""
}

// hasDeclarationComment also enforces the conventional prefix on exported declarations.
func hasDeclarationComment(group *ast.CommentGroup, name string) bool {
	if !hasComment(group) {
		return false
	}
	if ast.IsExported(name) {
		text := strings.TrimSpace(group.Text())
		return strings.HasPrefix(text, name+" ") || strings.HasPrefix(text, name+"\n")
	}
	return true
}

// appendCommentProblem 以相对路径和行号记录稳定、可定位的审计错误。
func appendCommentProblem(fileSet *token.FileSet, position token.Pos, label string, problems *[]string) {
	location := fileSet.Position(position)
	*problems = append(*problems, fmt.Sprintf("%s:%d: %s", location.Filename, location.Line, label))
}

// TestCommentAuditAcceptsEnglishAndRejectsEmptyComments checks language-neutral declaration coverage.
func TestCommentAuditAcceptsEnglishAndRejectsEmptyComments(t *testing.T) {
	for _, tc := range []struct {
		// source is a complete handwritten Go file.
		source string
		// wantProblems counts declarations lacking valid documentation.
		wantProblems int
	}{
		{"package p\n// Exported describes the operation.\nfunc Exported() {}", 0},
		{"package p\n//\nfunc hidden() {}", 1},
		{"package p\nfunc hidden() {}", 1},
		{"package p\n// Wrong describes the operation.\nfunc Exported() {}", 1},
	} {
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, "example.go", tc.source, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		var problems []string
		auditHandwrittenFile(fileSet, file, &problems)
		if len(problems) != tc.wantProblems {
			t.Fatalf("%q: got %v, want %d problems", tc.source, problems, tc.wantProblems)
		}
	}
}
