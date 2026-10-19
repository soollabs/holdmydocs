package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// userTextField matches struct fields that hold text a person reads in the
// rendered page. HTTP error bodies are call arguments, not fields, so they are
// out of scope here: they stay English by design.
var userTextField = regexp.MustCompile(`(Title|Heading|Label|Error|Message|Notice|Context|Subpage|Warning|Text|Description|Note|Hint|Age)$`)

// TestGoHasNoRawEnglish keeps page text set from Go in the catalogue: a string
// literal with words, assigned to a user-text field in a struct literal or an
// assignment, must be an i18n message instead.
func TestGoHasNoRawEnglish(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	check := func(field string, value ast.Expr) {
		lit, ok := value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !userTextField.MatchString(field) {
			return
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil || !hasWord.MatchString(text) {
			return
		}
		t.Errorf("%s: raw English %q in %s; add a message and use i18n.T", fset.Position(lit.Pos()), text, field)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok {
					check(key.Name, n.Value)
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && i < len(n.Rhs) {
						check(sel.Sel.Name, n.Rhs[i])
					}
				}
			}
			return true
		})
	}
}
