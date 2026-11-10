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
	"kernel/wshub",
	"logging",
	"platform/csrf",
	"platform/rediscsrf",
	"platform/idempotency",
	"platform/jobs",
	"platform/jobs/admin",
	"platform/jobs/jobutil",
	"platform/jobs/migrations",
	"platform/pgjobs",
	"platform/jwtauth",
	"platform/migrate",
	"platform/pgmigrate",
	"platform/objectstore",
	"platform/objectstore/localfs",
	"platform/objectstore/s3store",
	"platform/observability/oteltrace",
	"platform/observability/prommetrics",
	"platform/oidcauth",
	"platform/pgdb",
	"platform/ratelimit",
	"platform/redislimit",
	"platform/redisclient",
	"platform/redislock",
	"platform/session",
	"platform/redissession",
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
					copy := *spec
					var omitted bool
					copy.Type, omitted = exportedType(copy.Type)
					clearComments(&copy)
					declaration := "type " + formatNode(fset, &copy)
					if omitted {
						declaration = markUnexportedFieldsOmitted(declaration)
					}
					declarations = append(declarations, declaration)
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

func exportedType(expr ast.Expr) (ast.Expr, bool) {
	switch current := expr.(type) {
	case *ast.StructType:
		if current == nil || current.Fields == nil {
			return current, false
		}
		fields := &ast.FieldList{Opening: current.Fields.Opening, Closing: current.Fields.Closing}
		for _, field := range current.Fields.List {
			if exported := exportedStructField(field); exported != nil {
				fields.List = append(fields.List, exported)
			}
		}
		copy := *current
		copy.Fields = fields
		return &copy, len(fields.List) == 0 && len(current.Fields.List) > 0
	case *ast.ParenExpr:
		copy := *current
		var omitted bool
		copy.X, omitted = exportedType(current.X)
		return &copy, omitted
	default:
		return expr, false
	}
}

func markUnexportedFieldsOmitted(declaration string) string {
	const marker = "// unexported fields omitted"
	closing := strings.LastIndex(declaration, "}")
	if closing < 0 {
		return declaration
	}
	body := strings.TrimRight(declaration[:closing], " \t\r\n")
	if !strings.HasSuffix(body, "{") {
		return declaration
	}
	return body + "\n\t" + marker + "\n" + declaration[closing:]
}

func exportedStructField(field *ast.Field) *ast.Field {
	if field == nil {
		return nil
	}
	copy := *field
	if len(field.Names) == 0 {
		if !exportedTypeName(field.Type) {
			return nil
		}
		return &copy
	}
	names := make([]*ast.Ident, 0, len(field.Names))
	for _, name := range field.Names {
		if ast.IsExported(name.Name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	copy.Names = names
	return &copy
}

// exportedReceiver reports whether a method belongs to a type that callers can
// name outside the package. Exported methods on unexported helper types are
// implementation details, even though they can satisfy an exported interface.
func exportedReceiver(receiver *ast.FieldList) bool {
	if receiver == nil || len(receiver.List) == 0 {
		return true
	}
	return exportedTypeName(receiver.List[0].Type)
}

func exportedTypeName(typeExpr ast.Expr) bool {
	for {
		switch current := typeExpr.(type) {
		case *ast.StarExpr:
			typeExpr = current.X
		case *ast.IndexExpr:
			typeExpr = current.X
		case *ast.IndexListExpr:
			typeExpr = current.X
		case *ast.SelectorExpr:
			return ast.IsExported(current.Sel.Name)
		default:
			name, ok := typeExpr.(*ast.Ident)
			return ok && ast.IsExported(name.Name)
		}
	}
}

func TestDeclarationsForFileExportsOnlyPublicStructFields(t *testing.T) {
	source := `package sample

type Exported struct {
	Public string
	private string
	PublicAndPrivate, hidden int
	hiddenAgain, PublicAgain int
	ExportedEmbedded
	*pkg.ExternalEmbedded
	*pkg.hiddenEmbedded
	*hiddenEmbedded
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	declarations := declarationsForFile(fset, file)
	if len(declarations) != 1 {
		t.Fatalf("declarations = %#v", declarations)
	}
	declaration := declarations[0]
	for _, want := range []string{"type Exported struct", "Public", "PublicAndPrivate", "PublicAgain", "ExportedEmbedded", "pkg.ExternalEmbedded"} {
		if !strings.Contains(declaration, want) {
			t.Fatalf("declaration missing %q: %s", want, declaration)
		}
	}
	for _, unwanted := range []string{"private", "hidden", "hiddenAgain", "hiddenEmbedded"} {
		if strings.Contains(declaration, unwanted) {
			t.Fatalf("declaration leaked unexported field %q: %s", unwanted, declaration)
		}
	}
}

func TestDeclarationsForFileMarksUnexportedOnlyStructFields(t *testing.T) {
	source := `package sample

type PrivateOnly struct {
	hidden string
}

type Empty struct{}

type PublicOnly struct {
	Value string
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	declarations := declarationsForFile(fset, file)
	if len(declarations) != 3 {
		t.Fatalf("declarations = %#v", declarations)
	}
	byName := make(map[string]string, len(declarations))
	for _, declaration := range declarations {
		fields := strings.Fields(declaration)
		if len(fields) < 2 {
			t.Fatalf("malformed declaration: %q", declaration)
		}
		byName[fields[1]] = declaration
	}

	const marker = "// unexported fields omitted"
	if got := byName["PrivateOnly"]; !strings.Contains(got, marker) {
		t.Fatalf("private-only struct missing omission marker: %s", got)
	}
	for _, name := range []string{"Empty", "PublicOnly"} {
		if got := byName[name]; strings.Contains(got, marker) {
			t.Fatalf("%s unexpectedly has omission marker: %s", name, got)
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
