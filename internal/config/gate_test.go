package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestNoEnvReadsOutsideConfig is the WS1 static gate: internal/config is the
// only server package allowed to read the process environment. It walks every
// non-test Go file under cmd/ and internal/ and fails on os.Getenv /
// os.LookupEnv / os.Environ calls and on kong `env:"..."` struct tags.
//
// Allowlisted:
//   - internal/config itself (this package is the reader).
//   - cmd/arti — the CLI is a client with its own small env surface
//     (ARTI_TOKEN, ARTI_BASE_URL, self-update opt-out); it is rationalized
//     with the CLI work in the OSS Workstream 3. Nothing new should be
//     added there either.
func TestNoEnvReadsOutsideConfig(t *testing.T) {
	root := moduleRoot(t)
	var violations []string

	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			if strings.HasPrefix(rel, filepath.Join("internal", "config")+string(filepath.Separator)) ||
				strings.HasPrefix(rel, filepath.Join("cmd", "arti")+string(filepath.Separator)) &&
					!strings.HasPrefix(rel, filepath.Join("cmd", "arti-server")+string(filepath.Separator)) {
				return nil
			}
			violations = append(violations, scanFile(t, path, rel)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if len(violations) > 0 {
		t.Errorf("environment access outside internal/config (route it through the typed config instead):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func scanFile(t *testing.T, path, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if ident, ok := node.X.(*ast.Ident); ok && ident.Name == "os" {
				switch node.Sel.Name {
				case "Getenv", "LookupEnv", "Environ":
					out = append(out, fmt.Sprintf("%s:%d os.%s", rel, fset.Position(node.Pos()).Line, node.Sel.Name))
				}
			}
		case *ast.StructType:
			for _, field := range node.Fields.List {
				if field.Tag == nil {
					continue
				}
				tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))
				if v, ok := tag.Lookup("env"); ok {
					out = append(out, fmt.Sprintf("%s:%d env tag %s", rel, fset.Position(field.Pos()).Line, v))
				}
			}
		}
		return true
	})
	return out
}

// moduleRoot walks up from this package's directory to the go.mod root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from package dir")
		}
		dir = parent
	}
}
