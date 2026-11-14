package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// The offline table of aictl.md section 4.29 (AICTL-10): every command in the column "Works with no
// network at all" is run against a fake agent with the network blocked, and none of them may try to
// leave the computer. The block is process wide for the length of the test: the default HTTP transport
// and the default resolver fail the test (and record who asked), so a command that downloads, resolves
// a host name or calls an HTTP API on the internet is caught. The agent's own socket and loopback
// address are untouched, as in production.
//
// A command that needs the internet by design (download, ark sync, catalog refresh, update, cloud
// enroll's connection, cloud resend-key, rebuild-llama without a local source) is the second column
// of the table and is not run here.

type netGuard struct {
	mu       sync.Mutex
	attempts []string
}

func (g *netGuard) note(what string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attempts = append(g.attempts, what)
}

func (g *netGuard) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.attempts...)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// blockTheNetwork makes every request through the default HTTP transport and every DNS lookup fail and
// be recorded, until the test ends.
func blockTheNetwork(t *testing.T) *netGuard {
	t.Helper()
	g := &netGuard{}
	oldTransport, oldResolver := http.DefaultTransport, net.DefaultResolver
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		g.note("http " + r.Method + " " + r.URL.Host)
		return nil, errors.New("the network is blocked in this test")
	})
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
		g.note("dns via " + addr)
		return nil, errors.New("the network is blocked in this test")
	}}
	t.Cleanup(func() { http.DefaultTransport, net.DefaultResolver = oldTransport, oldResolver })
	return g
}

// offlineCase is one invocation of a command of the first column.
type offlineCase struct {
	command string // the top-level command of the column
	name    string
	// agent says the command talks to the agent: it must have reached the fake. Otherwise it is a
	// local command that must not need the agent at all.
	agent bool
	run   func(t *testing.T) runResult
}

func apiCase(command string, args ...string) offlineCase {
	return offlineCase{command: command, name: strings.Join(args, " "), agent: true, run: func(t *testing.T) runResult {
		r := newMgRig(t, fake.Options{})
		r.events()
		got := r.run(mgArgs(append([]string{"--yes"}, args...)...))
		if len(r.requestLines()) == 0 {
			t.Errorf("the fake agent was never reached")
		}
		return runResult(got)
	}}
}

func localCase(command, name string, run func(t *testing.T) runResult) offlineCase {
	return offlineCase{command: command, name: name, run: run}
}

func offlineCases() []offlineCase {
	local := func(args ...string) func(*testing.T) runResult {
		return func(t *testing.T) runResult { return runCLI(t, DefaultRegistry, localOpts(nil), args...) }
	}
	return []offlineCase{
		apiCase("status", "status"),
		apiCase("models", "models"),
		apiCase("load", "load", "qwen3.5-9b-q4km"),
		apiCase("unload", "unload", "qwen3.5-9b-q4km"),
		apiCase("workloads", "workloads"),
		apiCase("workloads", "workloads", "activate", "coding"),
		apiCase("default", "default"),
		apiCase("default", "default", "qwen3.5-9b-q4km"),
		apiCase("delete", "delete", "qwen3.5-9b-q4km"),
		apiCase("key", "key", "show"),
		apiCase("key", "key", "copy"),
		apiCase("key", "key", "rotate"),
		apiCase("key", "key", "snippets"),
		apiCase("mode", "mode"),
		apiCase("ark", "ark", "status"),
		apiCase("ark", "ark", "drives"),
		apiCase("ark", "ark", "verify"),
		apiCase("ark", "ark", "import", "--from", "ark-3f9c0a12"),
		apiCase("profile", "profile", "show"),
		apiCase("profile", "profile", "suggest"),
		apiCase("profile", "profile", "set", "laptop-8gb"),
		apiCase("services", "services"),
		apiCase("services", "services", "start", "kiwix"),
		apiCase("services", "services", "stop", "whisper"),
		apiCase("services", "services", "restart", "llama"),
		apiCase("restart", "restart", "llama"),
		apiCase("logs", "logs"),
		apiCase("doctor", "doctor"),
		apiCase("ensure", "ensure"),
		apiCase("drill", "drill"),
		apiCase("drill", "drill", "--cancel"),
		apiCase("drill", "drill", "show"),
		apiCase("drill", "drill", "record-manual", "--result", "pass"),
		apiCase("cloud", "cloud", "status"),
		apiCase("ui-code", "ui-code"),
		apiCase("bench", "bench", "qwen3.5-9b-q4km"),
		apiCase("version", "version"),

		localCase("pin", "pin set", func(t *testing.T) runResult {
			newLocalFixture(t)
			return runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "4821", "4821")), "pin", "set")
		}),
		localCase("token", "token rotate", func(t *testing.T) runResult {
			newLocalFixture(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "token", "rotate")
		}),
		localCase("catalog", "catalog lint", func(t *testing.T) runResult {
			newLocalFixture(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "catalog", "lint", writeLocalCatalog(t, t.TempDir(), "2026.11.03-1"))
		}),
		localCase("catalog", "catalog restore", func(t *testing.T) runResult {
			newLocalFixture(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "catalog", "restore")
		}),
		localCase("catalog", "catalog use --test", func(t *testing.T) runResult {
			newLocalFixture(t)
			draft := writeLocalCatalog(t, t.TempDir(), "2026.11.03-1")
			return runCLI(t, DefaultRegistry, localOpts(nil), "--yes", "catalog", "use", draft, "--test")
		}),
		localCase("backup-config", "backup-config", func(t *testing.T) runResult {
			newLocalFixture(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "backup-config", "--out", filepath.Join(t.TempDir(), "node.tar.gz"))
		}),
		localCase("docs", "docs render", func(t *testing.T) runResult {
			f := newDocsFixture(t, true)
			f.useEnv(t, nil)
			return runCLI(t, DefaultRegistry, runOpts{}, "--json", "docs", "render", "--src", f.src, "--out", filepath.Join(f.root, "out"), "--no-pdf")
		}),
		localCase("rebuild-llama", "rebuild-llama --dry-run", func(t *testing.T) runResult {
			newLlamaFix(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "rebuild-llama", "--from-kit", "--dry-run")
		}),
		localCase("rebuild-whisper", "rebuild-whisper --dry-run", func(t *testing.T) runResult {
			newWhisperFix(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "rebuild-whisper", "--from-kit", "--dry-run")
		}),
		localCase("rebuild-agent", "rebuild-agent --dry-run", func(t *testing.T) runResult {
			newAgentFix(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "rebuild-agent", "--from-kit", "--dry-run")
		}),
		localCase("kit", "kit status", func(t *testing.T) runResult { newKitFix(t); return local("kit", "status")(t) }),
		localCase("kit", "kit verify", func(t *testing.T) runResult { newKitFix(t); return local("kit", "verify")(t) }),
		localCase("kit", "kit export-build", func(t *testing.T) runResult { newExportFix(t); return local("kit", "export-build")(t) }),
		localCase("verify-release", "verify-release", func(t *testing.T) runResult {
			f := newReleaseFix(t)
			return runCLI(t, DefaultRegistry, localOpts(nil), "verify-release", f.dir)
		}),
	}
}

func TestEveryOfflineCommandRunsWithTheNetworkBlocked(t *testing.T) {
	applyLate(DefaultRegistry)
	for _, tc := range offlineCases() {
		t.Run(tc.command+": "+tc.name, func(t *testing.T) {
			guard := blockTheNetwork(t)
			got := tc.run(t)
			if seen := guard.seen(); len(seen) > 0 {
				t.Errorf("the command tried to use the network: %v\nstdout: %s\nstderr: %s", seen, got.out, got.errOut)
			}
			if got.code == ExitUnreachable && !tc.agent {
				t.Errorf("a local command needed the agent (exit 3)\n%s", got.errOut)
			}
			if got.code == ExitTimeout {
				t.Errorf("the command timed out\n%s", got.errOut)
			}
		})
	}
}

// ---- the table in the spec ---------------------------------------------------------------------------

// offlineCell is the first cell of the table of aictl.md section 4.29.
func offlineCell(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../docs/specs/aictl.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	start := strings.Index(text, "### 4.29 Offline behaviour summary")
	if start < 0 {
		t.Fatal("section 4.29 not found")
	}
	for _, l := range strings.Split(text[start:], "\n") {
		if strings.HasPrefix(l, "| ") && !strings.HasPrefix(l, "| Works") && !strings.HasPrefix(l, "|---") {
			return strings.TrimSpace(splitRow(l)[1])
		}
	}
	t.Fatal("the table of section 4.29 not found")
	return ""
}

var offlineItemWord = regexp.MustCompile(`^[a-z][a-z0-9-]*`)

// offlineCommands reads the command words of the first cell: "status, models, ... key show/copy/rotate,
// ... rebuild-llama/rebuild-whisper (kit source), ..." gives status, models, ..., key, ...,
// rebuild-llama, rebuild-whisper, ... The items are separated by commas outside brackets.
func offlineCommands(cell string) []string {
	var items []string
	depth, from := 0, 0
	for i, r := range cell {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, strings.TrimSpace(cell[from:i]))
				from = i + 1
			}
		}
	}
	items = append(items, strings.TrimSpace(cell[from:]))
	var words []string
	for _, it := range items {
		first := offlineItemWord.FindString(it)
		if first == "" {
			continue
		}
		words = append(words, first)
		// "rebuild-llama/rebuild-whisper (kit source)": the word after the slash is a command too.
		rest := strings.TrimPrefix(it, first)
		if strings.HasPrefix(rest, "/rebuild-") {
			words = append(words, offlineItemWord.FindString(strings.TrimPrefix(rest, "/")))
		}
	}
	return words
}

func TestTheOfflineTableOfTheSpecIsWhatTheTestRuns(t *testing.T) {
	spec := map[string]bool{}
	for _, w := range offlineCommands(offlineCell(t)) {
		spec[w] = true
	}
	if len(spec) < 25 {
		t.Fatalf("only %d commands were read from the offline table: %v", len(spec), spec)
	}
	ran := map[string]bool{}
	for _, tc := range offlineCases() {
		ran[tc.command] = true
	}
	var missing, extra []string
	for w := range spec {
		if !ran[w] {
			missing = append(missing, w)
		}
	}
	for w := range ran {
		if !spec[w] {
			extra = append(extra, w)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("aictl.md section 4.29 says these work with no network but offline_test.go does not run them: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("offline_test.go runs these as offline commands but section 4.29 does not list them: %v", extra)
	}
	// Each of them is a real command.
	reg := registeredCommands()
	for w := range spec {
		if _, ok := reg[w]; !ok {
			t.Errorf("the offline table names %q, which is not a command", w)
		}
	}
}

// The block itself works: a request through the default client and a name lookup are both caught.
func TestTheNetworkBlockCatchesAnAttempt(t *testing.T) {
	guard := blockTheNetwork(t)
	if resp, err := http.Get("http://offline-check.example.test/"); err == nil {
		resp.Body.Close()
		t.Fatal("the request went out")
	}
	if _, err := net.LookupHost("offline-check.example.test"); err == nil {
		t.Fatal("the lookup went out")
	}
	seen := strings.Join(guard.seen(), "|")
	if !strings.Contains(seen, "http GET offline-check.example.test") || !strings.Contains(seen, "dns via") {
		t.Errorf("attempts not recorded: %q", seen)
	}
}
