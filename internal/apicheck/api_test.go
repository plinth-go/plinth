package apicheck

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var publicPackages = []string{
	"app",
	"kernel",
	"kernel/auth",
	"kernel/authz",
	"kernel/cors",
	"kernel/uploads",
	"kernel/websocket",
	"logging",
	"platform/csrf",
	"platform/csrf/redis",
	"platform/idempotency",
	"platform/jobs",
	"platform/jobs/admin",
	"platform/jobs/migrations",
	"platform/jwt",
	"platform/migrate",
	"platform/objectstore",
	"platform/objectstore/localfs",
	"platform/objectstore/s3",
	"platform/observability/otel",
	"platform/observability/prometheus",
	"platform/oidc",
	"platform/postgres",
	"platform/ratelimit",
	"platform/ratelimit/redis",
	"platform/redis",
	"platform/redislock",
	"platform/session",
	"platform/session/redis",
	"runtime",
	"utils",
	"webui",
}

// TestPublicAPISurface freezes the importable API before a release. The
// snapshot is intentionally a readable declaration list, not a test of Go
// type-checking or standard-library behavior. Run with UPDATE_API=1 after an
// intentional API change and review the diff before committing.
func TestPublicAPISurface(t *testing.T) {
	root := moduleRoot(t)
	got := collectSurface(t, root)
	path := filepath.Join(root, "internal", "apicheck", "testdata", "public-api.txt")

	if os.Getenv("UPDATE_API") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read API snapshot: %v\nrun UPDATE_API=1 go test ./internal/apicheck -run TestPublicAPISurface", err)
	}
	if bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		return
	}
	t.Fatalf("public API surface changed; review the diff and run UPDATE_API=1 only for intentional changes")
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate API surface test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func collectSurface(t *testing.T, root string) []byte {
	t.Helper()
	var output strings.Builder
	for _, packageDir := range publicPackages {
		entries, err := os.ReadDir(filepath.Join(root, packageDir))
		if err != nil {
			t.Fatalf("read package %s: %v", packageDir, err)
		}

		fset := token.NewFileSet()
		var declarations []string
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(root, packageDir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s/%s: %v", packageDir, name, err)
			}
			declarations = append(declarations, declarationsForFile(fset, file)...)
		}

		sort.Strings(declarations)
		output.WriteString("package github.com/plinth-go/plinth/")
		output.WriteString(packageDir)
		output.WriteByte('\n')
		for _, declaration := range declarations {
			output.WriteString(declaration)
			output.WriteByte('\n')
		}
		output.WriteByte('\n')
	}
	return []byte(strings.TrimRight(output.String(), "\n") + "\n")
}

func declarationsForFile(fset *token.FileSet, file *ast.File) []string {
	var declarations []string
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range declaration.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if !ast.IsExported(spec.Name.Name) {
						continue
					}
					clearComments(spec)
					declarations = append(declarations, "type "+formatNode(fset, spec))
				case *ast.ValueSpec:
					if !hasExportedName(spec.Names) {
						continue
					}
					clearComments(spec)
					declarations = append(declarations, declaration.Tok.String()+" "+formatNode(fset, spec))
				}
			}
		case *ast.FuncDecl:
			if !ast.IsExported(declaration.Name.Name) || !exportedReceiver(declaration.Recv) {
				continue
			}
			copy := *declaration
			copy.Body = nil
			clearComments(&copy)
			declarations = append(declarations, formatNode(fset, &copy))
		}
	}
	return declarations
}

// exportedReceiver reports whether a method belongs to a type that callers can
// name outside the package. Exported methods on unexported helper types are
// implementation details, even though they can satisfy an exported interface.
func exportedReceiver(receiver *ast.FieldList) bool {
	if receiver == nil || len(receiver.List) == 0 {
		return true
	}
	typeExpr := receiver.List[0].Type
	for {
		switch current := typeExpr.(type) {
		case *ast.StarExpr:
			typeExpr = current.X
		case *ast.IndexExpr:
			typeExpr = current.X
		case *ast.IndexListExpr:
			typeExpr = current.X
		default:
			name, ok := typeExpr.(*ast.Ident)
			return ok && ast.IsExported(name.Name)
		}
	}
}

func TestExportedReceiver(t *testing.T) {
	method := func(receiver ast.Expr) *ast.FieldList {
		if receiver == nil {
			return nil
		}
		return &ast.FieldList{List: []*ast.Field{{Type: receiver}}}
	}
	tests := []struct {
		name     string
		receiver *ast.FieldList
		want     bool
	}{
		{name: "function", want: true},
		{name: "exported value", receiver: method(ast.NewIdent("Client")), want: true},
		{name: "exported pointer", receiver: method(&ast.StarExpr{X: ast.NewIdent("Client")}), want: true},
		{name: "unexported value", receiver: method(ast.NewIdent("client"))},
		{name: "unexported pointer", receiver: method(&ast.StarExpr{X: ast.NewIdent("client")})},
		{name: "exported generic", receiver: method(&ast.IndexExpr{
			X:     ast.NewIdent("Client"),
			Index: ast.NewIdent("T"),
		}), want: true},
		{name: "exported generic pointer", receiver: method(&ast.StarExpr{X: &ast.IndexExpr{
			X:     ast.NewIdent("Client"),
			Index: ast.NewIdent("T"),
		}}), want: true},
		{name: "unexported generic", receiver: method(&ast.IndexListExpr{
			X:       ast.NewIdent("client"),
			Indices: []ast.Expr{ast.NewIdent("K"), ast.NewIdent("V")},
		})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exportedReceiver(tt.receiver); got != tt.want {
				t.Fatalf("exportedReceiver() = %v, want %v", got, tt.want)
			}
		})
	}
}

func hasExportedName(names []*ast.Ident) bool {
	for _, name := range names {
		if ast.IsExported(name.Name) {
			return true
		}
	}
	return false
}

func clearComments(node ast.Node) {
	ast.Inspect(node, func(current ast.Node) bool {
		switch current := current.(type) {
		case *ast.Field:
			current.Doc = nil
			current.Comment = nil
		case *ast.TypeSpec:
			current.Doc = nil
			current.Comment = nil
		case *ast.ValueSpec:
			current.Doc = nil
			current.Comment = nil
		case *ast.FuncDecl:
			current.Doc = nil
		case *ast.GenDecl:
			current.Doc = nil
		}
		return true
	})
}

func formatNode(fset *token.FileSet, node ast.Node) string {
	var buffer bytes.Buffer
	if err := format.Node(&buffer, fset, node); err != nil {
		panic(err)
	}
	return strings.TrimSpace(buffer.String())
}
