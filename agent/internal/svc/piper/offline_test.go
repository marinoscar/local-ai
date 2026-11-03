package piper

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Offline first (AGENTS.md rule 3): speech works with the network cable pulled, because the package
// never opens a connection. Piper and the voices come from the kit and the ark; nothing is fetched
// at run time. This test reads the package's own sources and refuses the ways of dialling out.
func TestPackageHasNoNetworkClient(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{
		"Dial": true, "DialTimeout": true, "DialContext": true, "Get": true, "Post": true, "Head": true,
		"PostForm": true, "NewRequest": true, "NewRequestWithContext": true, "Client": true,
		"DefaultClient": true, "DefaultTransport": true, "Transport": true, "Listen": true, "LookupHost": true,
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			switch imp.Path.Value {
			case `"net"`, `"crypto/tls"`, `"net/rpc"`, `"net/smtp"`:
				t.Errorf("%s imports %s: the voice service must never touch the network", f, imp.Path.Value)
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "http" || id.Name == "net") && banned[sel.Sel.Name] {
				t.Errorf("%s: %s.%s is a way to open a connection", f, id.Name, sel.Sel.Name)
			}
			return true
		})
	}
}

// The text of a request is spoken, never logged: no slog call of the handler or the exec code takes
// the text as a value. The tests above prove it for the paths they exercise; this reads the sources
// for the one rule that is easy to break later, "log chars, never the text".
func TestNoLogCallMentionsTheText(t *testing.T) {
	for _, f := range []string{"handler.go", "exec.go", "probe.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "s.log.") && !strings.Contains(line, "log.Info") && !strings.Contains(line, "log.Error") && !strings.Contains(line, "log.Warn") {
				continue
			}
			for _, bad := range []string{"req.Text", "spoken", "\"text\"", "text,", "text)"} {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d logs the text (%s): %s", f, i+1, bad, strings.TrimSpace(line))
				}
			}
		}
	}
}
