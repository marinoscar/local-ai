package netx

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The guard of the network fence (drill.md section 4.4, DRILL-02). A client that builds its own
// http.Transport, rides on http.DefaultTransport (which every http.Client without a Transport does) or
// dials with net.Dialer reaches the internet during a drill without the fence seeing it, so the drill
// would report a node as offline-ready that is not. This test fails on any of them in the agent's
// non-test code, outside this package.
//
// What it flags, in every non-test .go file under agent/ (vendor excluded):
//   - the identifiers http.DefaultTransport, http.DefaultClient, http.Get, http.Head, http.Post and
//     http.PostForm;
//   - a composite literal or new() of http.Transport;
//   - a composite literal of http.Client that sets no Transport (it would use http.DefaultTransport);
//   - any use of net.Dialer, and net.Dial, net.DialTimeout, net.DialTCP, net.DialUDP and net.DialIP,
//     except net.Dial and net.DialTimeout to a literal "unix..." network;
//   - tls.Dial and tls.DialWithDialer.
//
// Use netx.Shared, netx.Client, netx.NewTransport, netx.UnixTransport, netx.DialContext or netx.Wrap.

// exempt lists the code the guard does not scan, each with the reason it can never be reached during a
// drill. A path ending in / is a directory. Adding a line needs a reason a reviewer accepts: the
// agent daemon's own outbound traffic is never exempt.
var exempt = map[string]string{
	"internal/cli/":                 "aictl is a separate short-lived process: it talks to the daemon over its Unix socket or loopback, and its own internet calls (aictl update, rebuild-whisper) run in the operator's terminal, never inside the daemon that the drill fences",
	"internal/setup/":               "the installer and repair library run as their own process (install-node.sh, aiagent setup), before and apart from the daemon",
	"cmd/aiagent/setup/":            "the aiagent setup subcommand is the installer process, not the daemon",
	"internal/router/migrate/host/": "the one-off migration probe runs from the aiagent migrate subcommand in the operator's terminal, and dials loopback or the lab's public host by design",
	"internal/modes/nethttp.go":     "the connectivity prober measures whether the internet is reachable; fenced, it would report 'none' for the length of every drill and (after 30 minutes) raise LAI-NET-011. It uses the internet only to answer that question, never to serve a feature",
}

type violation struct {
	file string
	line int
	what string
}

func (v violation) String() string { return fmt.Sprintf("%s:%d: %s", v.file, v.line, v.what) }

// scanSource returns the violations of one file's source.
func scanSource(file string, src []byte) ([]violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		return nil, err
	}
	names := map[string]string{} // local import name -> import path
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		switch p {
		case "net/http", "net", "crypto/tls":
			n := filepath.Base(p)
			if im.Name != nil {
				n = im.Name.Name
			}
			names[n] = p
		}
	}
	// sel returns the import path and member of a package-qualified selector.
	sel := func(e ast.Expr) (path, member string) {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", ""
		}
		id, ok := s.X.(*ast.Ident)
		if !ok || id.Obj != nil { // a local variable named http, not the package
			return "", ""
		}
		return names[id.Name], s.Sel.Name
	}
	var out []violation
	add := func(p token.Pos, what string) {
		out = append(out, violation{file: file, line: fset.Position(p).Line, what: what})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			switch path, m := sel(x); {
			case path == "net/http" && (m == "DefaultTransport" || m == "DefaultClient" || m == "Get" || m == "Head" || m == "Post" || m == "PostForm"):
				add(x.Pos(), "http."+m+" bypasses the shared transport")
			case path == "net" && m == "Dialer":
				add(x.Pos(), "net.Dialer dials outside the fence")
			case path == "net" && (m == "DialTCP" || m == "DialUDP" || m == "DialIP"):
				add(x.Pos(), "net."+m+" dials outside the fence")
			case path == "crypto/tls" && (m == "Dial" || m == "DialWithDialer"):
				add(x.Pos(), "tls."+m+" dials outside the fence")
			}
		case *ast.CallExpr:
			if path, m := sel(x.Fun); path == "net" && (m == "Dial" || m == "DialTimeout") {
				unix := false
				if len(x.Args) > 0 {
					if bl, ok := x.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						s, _ := strconv.Unquote(bl.Value)
						unix = strings.HasPrefix(s, "unix")
					}
				}
				if !unix {
					add(x.Pos(), "net."+m+" dials outside the fence")
				}
			}
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
				if path, m := sel(x.Args[0]); path == "net/http" && m == "Transport" {
					add(x.Pos(), "new(http.Transport) is a transport the fence cannot see: use netx.NewTransport")
				}
			}
		case *ast.CompositeLit:
			path, m := sel(x.Type)
			if path != "net/http" {
				break
			}
			switch m {
			case "Transport":
				add(x.Pos(), "http.Transport{...} is a transport the fence cannot see: use netx.NewTransport or netx.Shared")
			case "Client":
				hasTransport := false
				for _, el := range x.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Transport" {
							hasTransport = true
						}
					}
				}
				if !hasTransport {
					add(x.Pos(), "http.Client{...} without a Transport rides on http.DefaultTransport: use netx.Client")
				}
			}
		}
		return true
	})
	return out, nil
}

func TestGuardRules(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int // violations
	}{
		{"default transport", `package a; import "net/http"; var _ = http.DefaultTransport`, 1},
		{"clone of default transport", `package a; import "net/http"; var _ = http.DefaultTransport.(*http.Transport).Clone()`, 1},
		{"default client", `package a; import "net/http"; var _ = http.DefaultClient`, 1},
		{"package get", `package a; import "net/http"; func f() { _, _ = http.Get("x") }`, 1},
		{"transport literal", `package a; import "net/http"; var _ = &http.Transport{Proxy: nil}`, 1},
		{"new transport", `package a; import "net/http"; var _ = new(http.Transport)`, 1},
		{"client without transport", `package a; import "net/http"; var _ = &http.Client{}`, 1},
		{"client with timeout only", `package a; import ("net/http";"time"); var _ = &http.Client{Timeout: time.Second}`, 1},
		{"client with a transport", `package a; import "net/http"; func f(rt http.RoundTripper) { _ = &http.Client{Transport: rt} }`, 0},
		{"var client", `package a; import "net/http"; func f() { var c http.Client; _ = c }`, 0},
		{"transport type in a signature", `package a; import "net/http"; func f(t *http.Transport) *http.Transport { return t }`, 0},
		{"dialer", `package a; import "net"; var _ = &net.Dialer{}`, 1},
		{"var dialer", `package a; import "net"; func f() { var d net.Dialer; _ = d }`, 1},
		{"net dial", `package a; import "net"; func f() { _, _ = net.Dial("tcp", "x:1") }`, 1},
		{"net dial timeout tcp", `package a; import ("net";"time"); func f() { _, _ = net.DialTimeout("tcp", "x:1", time.Second) }`, 1},
		{"net dial unix", `package a; import ("net";"time"); func f() { _, _ = net.DialTimeout("unix", "/x", time.Second) }`, 0},
		{"net dial unix variable", `package a; import "net"; func f(n string) { _, _ = net.Dial(n, "/x") }`, 1},
		{"dial unix", `package a; import "net"; func f() { _, _ = net.DialUnix("unixgram", nil, nil) }`, 0},
		{"tls dial", `package a; import "crypto/tls"; func f() { _, _ = tls.Dial("tcp", "x:1", nil) }`, 1},
		{"aliased import", `package a; import h "net/http"; var _ = h.DefaultTransport`, 1},
		{"a local called http", `package a; type T struct{ DefaultTransport int }; func f(http T) int { return http.DefaultTransport }`, 0},
		{"other package", `package a; import "example/other"; var _ = other.DefaultTransport`, 0},
	}
	for _, c := range cases {
		got, err := scanSource("x.go", []byte(c.src))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: %d violations, want %d: %v", c.name, len(got), c.want, got)
		}
	}
}

func TestExemptionsHaveReasons(t *testing.T) {
	for p, why := range exempt {
		if strings.TrimSpace(why) == "" {
			t.Errorf("exemption %s has no reason", p)
		}
		if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(p))); err != nil {
			t.Errorf("exemption %s names nothing: %v", p, err)
		}
	}
}

func isExempt(rel string) bool {
	for p := range exempt {
		if rel == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p)) {
			return true
		}
	}
	return false
}

// TestNoOutboundClientBypassesTheFence is the guard itself.
func TestNoOutboundClientBypassesTheFence(t *testing.T) {
	root := filepath.Join("..", "..") // agent/
	var bad []violation
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "vendor" || rel == "internal/netx" || strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || isExempt(rel) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		vs, err := scanSource(rel, src)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		scanned++
		bad = append(bad, vs...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("the guard scanned only %d files: is the root right?", scanned)
	}
	if len(bad) > 0 {
		sort.Slice(bad, func(i, j int) bool { return bad[i].String() < bad[j].String() })
		lines := make([]string, len(bad))
		for i, v := range bad {
			lines[i] = v.String()
		}
		t.Fatalf("%d outbound client(s) bypass the network fence (drill.md section 4.4); use netx:\n  %s", len(bad), strings.Join(lines, "\n  "))
	}
}
