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
	"unicode"
)

// TestHandwrittenGoDeclarationsHaveChineseComments 锁住 V1 对手写声明的中文注释要求。
// 生成协议保留上游原始文档，因此只按 Code generated 标记豁免，不依赖文件名猜测。
func TestHandwrittenGoDeclarationsHaveChineseComments(t *testing.T) {
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
	t.Fatalf("以下手写 Go 声明缺少中文注释:\n%s", strings.Join(problems, "\n"))
}

// shouldSkipCommentAuditDirectory 判断不属于本仓库手写 Go 源码的目录。
func shouldSkipCommentAuditDirectory(name string) bool {
	switch name {
	case ".git", ".upstream", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

// auditHandwrittenFile 检查类型、函数以及结构体和接口字段的声明注释。
func auditHandwrittenFile(fileSet *token.FileSet, file *ast.File, problems *[]string) {
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if !hasChineseComment(typed.Doc) {
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
				if !hasChineseComment(comment) {
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
		if hasChineseComment(field.Doc) || hasChineseComment(field.Comment) {
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

// hasChineseComment 判断注释是否至少包含一个汉字，避免英文占位注释绕过 A18。
func hasChineseComment(group *ast.CommentGroup) bool {
	if group == nil {
		return false
	}
	for _, character := range group.Text() {
		if unicode.Is(unicode.Han, character) {
			return true
		}
	}
	return false
}

// appendCommentProblem 以相对路径和行号记录稳定、可定位的审计错误。
func appendCommentProblem(fileSet *token.FileSet, position token.Pos, label string, problems *[]string) {
	location := fileSet.Position(position)
	*problems = append(*problems, fmt.Sprintf("%s:%d: %s", location.Filename, location.Line, label))
}
