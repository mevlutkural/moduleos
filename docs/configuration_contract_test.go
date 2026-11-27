package docs_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Keep the canonical reference complete when the loader gains a supported key.
// This checks inventory, not the accuracy of prose or live operator procedures.
func TestConfigurationReferenceInventory(t *testing.T) {
	source, err := parser.ParseFile(token.NewFileSet(), "../apps/control-plane/internal/config/config.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	ast.Inspect(source, func(node ast.Node) bool {
		value, ok := node.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || value.Names[0].Name != "environmentKeys" {
			return true
		}
		if len(value.Values) != 1 {
			t.Fatal("environmentKeys must have one initializer")
		}
		literal, ok := value.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatal("environmentKeys is no longer a literal; update the documentation inventory check")
		}
		for _, element := range literal.Elts {
			basic, ok := element.(*ast.BasicLit)
			if !ok || basic.Kind != token.STRING {
				t.Fatal("environmentKeys must contain string literals")
			}
			key, err := strconv.Unquote(basic.Value)
			if err != nil {
				t.Fatal(err)
			}
			keys = append(keys, key)
		}
		return false
	})
	if len(keys) == 0 {
		t.Fatal("no runtime keys found")
	}
	content, err := os.ReadFile("control-plane/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	tables := strings.SplitN(string(content), "## Operator env file", 2)
	if len(tables) != 2 {
		t.Fatal("missing operator env-file boundary")
	}
	check := func(key, table string) {
		t.Helper()
		row := "| `" + key + "` |"
		if strings.Count(table, row) != 1 {
			t.Errorf("expected one canonical table row for %s", key)
		}
	}
	for _, key := range keys {
		check(key, tables[0])
	}
	example, err := os.ReadFile("../deploy/swarm/moduleos.env.example")
	if err != nil {
		t.Fatal(err)
	}
	operatorKeys := regexp.MustCompile(`(?m)^(?:# )?(MODULEOS_[A-Z_]+)=`).FindAllStringSubmatch(string(example), -1)
	if len(operatorKeys) == 0 {
		t.Fatal("no operator keys found")
	}
	for _, key := range operatorKeys {
		check(key[1], tables[1])
	}
	rows := regexp.MustCompile("(?m)^\\| `([A-Z_]+)` \\|")
	if len(rows.FindAllStringSubmatch(tables[0], -1)) != len(keys) ||
		len(rows.FindAllStringSubmatch(tables[1], -1)) != len(operatorKeys) {
		t.Fatal("configuration tables contain an undocumented-source or misplaced key")
	}
}
