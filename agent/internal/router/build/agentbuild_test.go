package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// ---- the fixture: a node and a kit that rebuild the agent -----------------------------------

const (
	agentTag     = "node-v0.4.0"
	agentVersion = "0.4.0"
	agentModule  = "example.test/localai/agent"
	oldAgentVer  = "0.3.0"
	goVersion    = "1.24.7"
)

// agentRig is a node in temporary directories: a kit root with a source tarball and a Go toolchain,
// an installed agent, the scratch directory, and every program faked.
type agentRig struct {
	t       *testing.T
	kitRoot string
	scratch string
	opt     string
	exec    *fakeExec
	clk     *manualClock
	events  *eventLog
	audit   *auditRec
	fsys    *ownerFS

	arch    string
	options AgentOptions
	b       *AgentBuilder

	// What the fake programs answer.
	goMod       string
	goRC        int
	goErr       error
	goOut       string
	noOutput    bool
	elf         []byte
	newReports  string // `aiagent.new version --json`; "" is the right one
	newRC       int
	oldReports  string
	systemctlRC int
	restarts    int

	// The agent's health: the n-th read (1-based) is answered by health.
	health      func(n int) (AgentHealth, error)
	healthReads int
	restartedAt int // the number of health reads when the first restart happened

	srcTarball string
	goTarball  string
	oldBytes   []byte
	recorded   map[string]map[string]string
}

func goModText(mod, goLine string, requires ...string) string {
	s := "module " + mod + "\n\n" + goLine + "\n"
	if len(requires) > 0 {
		s += "\nrequire (\n"
		for _, r := range requires {
			s += "\t" + r + " v1.0.0\n"
		}
		s += ")\n"
	}
	return s
}

// sourceEntries is the agent source as the release writes it: no top-level folder, agent/ with its
// module, the command and the vendored packages, and a sibling folder.
func sourceEntries(mod string, drop ...string) []tarEntry {
	entries := []tarEntry{
		{name: "agent/go.mod", body: goModText(mod, "go 1.24", "github.com/example/dep")},
		{name: "agent/cmd/aiagent/main.go", body: "package main\nfunc main() {}\n"},
		{name: "agent/vendor/modules.txt", body: "# github.com/example/dep v1.0.0\n## explicit; go 1.20\ngithub.com/example/dep\ngithub.com/example/dep/sub\n"},
		{name: "agent/vendor/github.com/example/dep/dep.go", body: "package dep\n"},
		{name: "agent/vendor/github.com/example/dep/sub/sub.go", body: "package sub\n"},
		{name: "docs/README.md", body: "docs\n"},
	}
	var out []tarEntry
	for _, e := range entries {
		skip := false
		for _, d := range drop {
			skip = skip || e.name == d
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}

func newAgentRig(t *testing.T) *agentRig {
	t.Helper()
	r := &agentRig{
		t: t, kitRoot: t.TempDir(), scratch: filepath.Join(t.TempDir(), "build"), opt: t.TempDir(),
		exec: &fakeExec{}, clk: &manualClock{t: testNow}, events: &eventLog{}, audit: &auditRec{},
		arch: "amd64", elf: elfFixture(false, "", ""), goMod: agentModule,
		health: func(n int) (AgentHealth, error) { return AgentHealth{Version: agentVersion}, nil },
	}
	r.fsys = &ownerFS{OSFS: OSFS{}, log: r.events, root: r.scratch}
	if err := os.MkdirAll(filepath.Join(r.opt, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.oldBytes = []byte("the agent in use before the rebuild")
	r.write(filepath.Join(r.opt, "bin", "aiagent"), string(r.oldBytes))
	r.exec.on(r.world)
	r.source(agentTag, sourceEntries(agentModule)...)
	r.toolchain(goVersion)
	r.options = AgentOptions{
		KitRoots: func() []string { return []string{r.kitRoot} },
		Exec:     r.exec, FS: r.fsys, ScratchDir: r.scratch, OptRoot: r.opt, Arch: "amd64",
		Lookup: func(name string) (string, error) {
			switch name {
			case "setpriv", "systemctl":
				return "/usr/bin/" + name, nil
			}
			return "", execx.ErrNotFound
		},
		LookupUser: func(name string) (int, int, error) {
			if name != "localai" {
				return 0, 0, fmt.Errorf("unknown user %s", name)
			}
			return 1234, 2345, nil
		},
		Health: func(context.Context) (AgentHealth, error) {
			r.healthReads++
			return r.health(r.healthReads)
		},
		Audit: r.audit, Clock: r.clk.Now, Sleep: r.clk.Sleep,
		FreeBytes: func(string) (uint64, error) { return 10 << 30, nil },
	}
	return r
}

func (r *agentRig) write(path, content string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// record sets the digest of a file in the kit manifest of a part (kit/src or kit/toolchains), keeping the
// other files; recording a name again replaces it.
func (r *agentRig) record(part, name, sum string) {
	r.t.Helper()
	if r.recorded == nil {
		r.recorded = map[string]map[string]string{}
	}
	if r.recorded[part] == nil {
		r.recorded[part] = map[string]string{}
	}
	r.recorded[part][name] = sum
	var files []string
	var names []string
	for n := range r.recorded[part] {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		files = append(files, fmt.Sprintf(`{"kind":"file","path":%q,"sha256":%q}`, n, r.recorded[part][n]))
	}
	r.write(filepath.Join(r.kitRoot, "kit", part, "MANIFEST.json"), `{"schema_version":1,"files":[`+strings.Join(files, ",")+`]}`)
}

// source writes kit/src/local-ai-<tag>-src.tar.gz and records its digest.
func (r *agentRig) source(tag string, entries ...tarEntry) string {
	r.t.Helper()
	data := makeTar(r.t, entries, true)
	name := "local-ai-" + tag + "-src.tar.gz"
	p := filepath.Join(r.kitRoot, "kit", "src", name)
	r.write(p, string(data))
	r.record("src", name, sum256(data))
	r.srcTarball = p
	return p
}

// toolchain writes kit/toolchains/go<ver>.linux-<arch>.tar.gz (a top-level go/ folder, as go.dev's) and
// records its digest.
func (r *agentRig) toolchain(ver string) string {
	r.t.Helper()
	data := makeTar(r.t, []tarEntry{
		{name: "go/", typ: tar.TypeDir},
		{name: "go/VERSION", body: "go" + ver + "\n"},
		{name: "go/bin/", typ: tar.TypeDir},
		{name: "go/bin/go", body: "#!go\n", mode: 0o755},
	}, true)
	name := "go" + ver + ".linux-" + r.arch + ".tar.gz"
	p := filepath.Join(r.kitRoot, "kit", "toolchains", name)
	r.write(p, string(data))
	r.record("toolchains", name, sum256(data))
	r.goTarball = p
	return p
}

func (r *agentRig) builder() *AgentBuilder {
	r.options.Arch = r.arch
	r.b = NewAgentBuilder(r.options)
	return r.b
}

func (r *agentRig) build(req AgentRequest) (AgentResult, error) {
	r.t.Helper()
	if r.b == nil {
		r.builder()
	}
	return r.b.Build(context.Background(), req)
}

// world answers go, the new agent, the installed agent and systemctl.
func (r *agentRig) world(c execx.Cmd) (execx.Result, error, bool) {
	base := filepath.Base(c.Path)
	switch base {
	case "setpriv":
		if len(c.Args) > 5 && c.Args[0] == "--reuid" {
			inner := execx.Cmd{Path: c.Args[5], Args: c.Args[6:], Timeout: c.Timeout, MaxOut: c.MaxOut, Env: c.Env}
			r.events.add("setpriv %s", c.Args[1])
			return r.world(inner)
		}
	case "go":
		r.events.add("exec go %s", strings.Join(c.Args, " "))
		if r.goErr != nil {
			return execx.Result{}, r.goErr, true
		}
		if r.goRC != 0 {
			return execx.Result{ExitCode: r.goRC, Stderr: []byte(r.goOut)}, nil, true
		}
		if !r.noOutput {
			for i, a := range c.Args {
				if a == "-o" && i+1 < len(c.Args) {
					if err := os.WriteFile(c.Args[i+1], r.elf, 0o755); err != nil {
						return execx.Result{}, err, true
					}
				}
			}
		}
		return execx.Result{Stderr: []byte(r.goOut)}, nil, true
	case "aiagent.new", "aiagent":
		if len(c.Args) == 2 && c.Args[0] == "version" {
			r.events.add("exec %s version --json", base)
			rep, rc := r.newReports, r.newRC
			if base == "aiagent" {
				rep, rc = r.oldReports, 0
				if rep == "" {
					rep = fmt.Sprintf(`{"version":%q,"os":"linux","arch":%q}`, oldAgentVer, r.arch)
				}
			} else if rep == "" {
				rep = fmt.Sprintf(`{"version":%q,"os":"linux","arch":%q}`, agentVersion, r.arch)
			}
			return execx.Result{ExitCode: rc, Stdout: []byte(rep)}, nil, true
		}
	case "systemctl":
		r.events.add("exec systemctl %s", strings.Join(c.Args, " "))
		r.restarts++
		if r.restarts == 1 {
			r.restartedAt = r.healthReads
		}
		return execx.Result{ExitCode: r.systemctlRC}, nil, true
	}
	return execx.Result{}, nil, false
}

func (r *agentRig) bin(name string) string { return filepath.Join(r.opt, "bin", name) }

func (r *agentRig) readBin(name string) string {
	b, err := os.ReadFile(r.bin(name))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *agentRig) has(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func mustAgentErr(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want a *build.Error, got %T: %v", err, err)
	}
	return e
}

func failedAgent(rep AgentReport) []string {
	var out []string
	for _, c := range rep.Checks {
		if !c.OK {
			out = append(out, c.ID+":"+c.Code)
		}
	}
	return out
}

// ---- the happy path ---------------------------------------------------------------------------

func TestAgentBuildFromTheKitAndSwap(t *testing.T) {
	r := newAgentRig(t)
	var progress []string
	res, err := r.build(AgentRequest{Progress: func(p Progress) { progress = append(progress, p.Message) }})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Switched || !res.Restarted || res.Tag != agentTag || res.Version != agentVersion || res.Previous != oldAgentVer {
		t.Fatalf("result = %+v", res)
	}
	// The installed agent is the compiled program, the replaced one is kept, nothing is left next to them.
	if r.readBin("aiagent") != string(r.elf) {
		t.Error("aiagent is not the compiled program")
	}
	if r.readBin("aiagent.prev") != string(r.oldBytes) {
		t.Error("aiagent.prev is not the agent that was in use")
	}
	if r.has(r.bin("aiagent.new")) || r.has(r.bin("aiagent.prev.tmp")) {
		t.Error("a staging file was left next to the agent")
	}
	for _, p := range []string{"aiagent", "aiagent.prev"} {
		if fi, _ := os.Stat(r.bin(p)); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %v", p, fi.Mode().Perm())
		}
	}
	left, _ := os.ReadDir(r.scratch)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{".agent-build.lock", "agent-build.log"}) {
		t.Errorf("the scratch folder keeps %v (toolchain and tree are scratch)", names)
	}

	// The command is §4.3.4 step 3, run as the compile user.
	tree := filepath.Join(r.scratch, "local-ai-"+agentTag)
	var goCmd *execx.Cmd
	for _, c := range r.exec.all() {
		c := c
		if filepath.Base(c.Path) == "setpriv" {
			goCmd = &c
		}
	}
	if goCmd == nil {
		t.Fatalf("go did not run through setpriv: %v", r.exec.lines())
	}
	wantArgs := []string{"--reuid", "localai", "--regid", "localai", "--clear-groups", filepath.Join(r.scratch, "go", "bin", "go"),
		"build", "-C", filepath.Join(tree, "agent"), "-trimpath", "-ldflags", "-s -w -X " + agentModule + "/internal/app.Version=" + agentVersion,
		"-o", filepath.Join(tree, "aiagent.new"), "./cmd/aiagent"}
	if !reflect.DeepEqual(goCmd.Args, wantArgs) {
		t.Errorf("argv = %q\nwant   %q", goCmd.Args, wantArgs)
	}
	env := strings.Join(goCmd.Env, " ")
	for _, want := range []string{"GOTOOLCHAIN=local", "GOFLAGS=-mod=vendor", "CGO_ENABLED=0", "GOPROXY=off"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s: %v", want, goCmd.Env)
		}
	}
	// The tree belongs to the compile user while it compiles and to root again right after.
	ev := r.events.all()
	toUser, compile, back := r.events.index("chown local-ai-"+agentTag+" 1234:2345"), r.events.index("setpriv localai"), r.events.index("chown local-ai-"+agentTag+" 0:0")
	if toUser < 0 || compile < 0 || back < 0 || !(toUser < compile && compile < back) {
		t.Errorf("order of chown, compile and chown back: %d %d %d in %v", toUser, compile, back, ev)
	}
	// Smoke test, then the switch: the new agent was started once before aiagent was replaced, and the
	// agent was restarted and asked for its health afterwards.
	smoke, restart := r.events.index("exec aiagent.new version --json"), r.events.index("exec systemctl restart localai-agent.service")
	if smoke < 0 || restart < 0 || smoke > restart {
		t.Errorf("smoke %d, restart %d in %v", smoke, restart, ev)
	}
	if r.healthReads < 2 || r.restartedAt != 1 {
		t.Errorf("health reads %d, restarted after %d", r.healthReads, r.restartedAt)
	}
	// The log keeps the command and what the compiler said; the audit line names the switch.
	log := readFile(t, filepath.Join(r.scratch, "agent-build.log"))
	if !strings.Contains(log, "-trimpath") || !strings.Contains(log, "smoke: aiagent.new reports "+agentVersion) {
		t.Errorf("log = %q", log)
	}
	if len(r.audit.events) != 1 {
		t.Fatalf("audit = %+v", r.audit.events)
	}
	ev0 := r.audit.events[0]
	if ev0.Action != "agent.build.switch" || ev0.Result != audit.ResultOK || ev0.Target["version"] != agentVersion || ev0.Target["previous"] != oldAgentVer {
		t.Errorf("audit = %+v", ev0)
	}
	said := strings.Join(progress, " | ")
	for _, want := range []string{"Go toolchain", "agent source", "Compiling", "Testing", "Switching"} {
		if !strings.Contains(said, want) {
			t.Errorf("progress lacks %q: %s", want, said)
		}
	}
}

func TestAgentBuildOnANodeWithNoAgentInstalledYet(t *testing.T) {
	r := newAgentRig(t)
	if err := os.Remove(r.bin("aiagent")); err != nil {
		t.Fatal(err)
	}
	r.health = func(n int) (AgentHealth, error) {
		if n == 1 {
			return AgentHealth{}, errors.New("connection refused") // nothing runs yet
		}
		return AgentHealth{Version: agentVersion}, nil
	}
	res, err := r.build(AgentRequest{})
	if err != nil || !res.Switched || res.Previous != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if r.has(r.bin("aiagent.prev")) {
		t.Error("there was nothing to keep as aiagent.prev")
	}
	if r.readBin("aiagent") != string(r.elf) {
		t.Error("aiagent is not the compiled program")
	}
}

func TestAgentBuildWithNoRunningAgentOrUnitOnlyInstalls(t *testing.T) {
	r := newAgentRig(t)
	r.systemctlRC = 5 // Unit localai-agent.service not found
	r.health = func(int) (AgentHealth, error) { return AgentHealth{}, errors.New("connection refused") }
	res, err := r.build(AgentRequest{})
	if err != nil {
		t.Fatalf("a node rebuilt from scratch has nothing to restart: %v", err)
	}
	if !res.Switched || res.Restarted || res.RestartNote == "" {
		t.Fatalf("result = %+v", res)
	}
	if r.readBin("aiagent") != string(r.elf) || r.restarts != 1 {
		t.Errorf("installed %v, restarts %d", r.readBin("aiagent") == string(r.elf), r.restarts)
	}
}

func TestAgentBuildSelectsTheTag(t *testing.T) {
	r := newAgentRig(t)
	r.source("node-v0.5.0", sourceEntries(agentModule)...)
	r.source("node-v0.4.1-rc.1", sourceEntries(agentModule)...)
	r.newReports = fmt.Sprintf(`{"version":"0.5.0","os":"linux","arch":"amd64"}`)
	r.health = func(int) (AgentHealth, error) { return AgentHealth{Version: "0.5.0"}, nil }
	res, err := r.build(AgentRequest{})
	if err != nil || res.Tag != "node-v0.5.0" || res.Version != "0.5.0" {
		t.Fatalf("newest: %+v %v", res, err)
	}
	r2 := newAgentRig(t)
	r2.source("node-v0.5.0", sourceEntries(agentModule)...)
	rep, err := r2.builder().Check(context.Background(), AgentRequest{Tag: agentTag})
	if err != nil || rep.Tag != agentTag || !rep.OK() {
		t.Fatalf("explicit tag: %+v %v", rep, err)
	}
	rep, _ = r2.builder().Check(context.Background(), AgentRequest{Tag: "node-v9.9.9"})
	if got := failedAgent(rep); !reflect.DeepEqual(got, []string{"source:" + doctor.KIT040}) {
		t.Errorf("a tag the kit lacks: %v", got)
	}
}

// ---- --dry-run --------------------------------------------------------------------------------

func TestAgentDryRunChangesNothing(t *testing.T) {
	r := newAgentRig(t)
	res, err := r.build(AgentRequest{DryRun: true})
	if err != nil || !res.DryRun || res.Report == nil || !res.Report.OK() || res.Switched {
		t.Fatalf("%+v %v", res, err)
	}
	var ids []string
	for _, c := range res.Report.Checks {
		ids = append(ids, c.ID)
	}
	if want := []string{"source", "toolchain", "vendor", "go_version", "compile", "disk"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("checks = %v, want %v", ids, want)
	}
	if len(r.exec.all()) != 0 {
		t.Errorf("a dry run runs nothing: %v", r.exec.lines())
	}
	if r.has(r.scratch) {
		t.Error("a dry run creates no scratch folder")
	}
	if r.readBin("aiagent") != string(r.oldBytes) || r.has(r.bin("aiagent.prev")) || r.has(r.bin("aiagent.new")) {
		t.Error("a dry run touches the installed agent")
	}
	if len(r.audit.events) != 0 {
		t.Errorf("audit = %+v", r.audit.events)
	}
}

func TestAgentDryRunFindsWhatIsMissing(t *testing.T) {
	tests := []struct {
		name string
		set  func(r *agentRig)
		want []string
	}{
		{"no toolchain for this architecture", func(r *agentRig) { _ = os.Remove(r.goTarball) }, []string{"toolchain:" + doctor.KIT040}},
		{"no toolchain folder at all", func(r *agentRig) { _ = os.RemoveAll(filepath.Join(r.kitRoot, "kit", "toolchains")) }, []string{"toolchain:" + doctor.KIT040}},
		{"a toolchain for the other architecture only", func(r *agentRig) {
			_ = os.Remove(r.goTarball)
			r.arch = "arm64"
			r.toolchain(goVersion)
			r.arch = "amd64"
		}, []string{"toolchain:" + doctor.KIT040}},
		{"no source tarball", func(r *agentRig) { _ = os.Remove(r.srcTarball) }, []string{"source:" + doctor.KIT040}},
		{"a source nobody recorded", func(r *agentRig) { _ = os.Remove(filepath.Join(r.kitRoot, "kit", "src", "MANIFEST.json")) }, []string{"source:" + doctor.KIT001}},
		{"a toolchain nobody recorded", func(r *agentRig) { _ = os.Remove(filepath.Join(r.kitRoot, "kit", "toolchains", "MANIFEST.json")) }, []string{"toolchain:" + doctor.KIT001}},
		{"a source that was changed", func(r *agentRig) { r.write(r.srcTarball, "not the tarball that was recorded") }, []string{"source:" + doctor.KIT001}},
		{"a toolchain that was changed", func(r *agentRig) { r.write(r.goTarball, "not the toolchain that was recorded") }, []string{"toolchain:" + doctor.KIT001}},
		{"vendored package folder removed", func(r *agentRig) {
			r.source(agentTag, sourceEntries(agentModule, "agent/vendor/github.com/example/dep/sub/sub.go")...)
		}, []string{"vendor:" + doctor.KIT030}},
		{"vendor/modules.txt missing", func(r *agentRig) {
			r.source(agentTag, sourceEntries(agentModule, "agent/vendor/modules.txt")...)
		}, []string{"vendor:" + doctor.KIT030}},
		{"agent/go.mod missing", func(r *agentRig) {
			r.source(agentTag, sourceEntries(agentModule, "agent/go.mod")...)
		}, []string{"vendor:" + doctor.KIT030}},
		{"the command is missing", func(r *agentRig) {
			r.source(agentTag, sourceEntries(agentModule, "agent/cmd/aiagent/main.go")...)
		}, []string{"vendor:" + doctor.KIT030}},
		{"go.mod requires a module that is not vendored", func(r *agentRig) {
			e := sourceEntries(agentModule)
			e[0].body = goModText(agentModule, "go 1.24", "github.com/example/dep", "github.com/example/other")
			r.source(agentTag, e...)
		}, []string{"vendor:" + doctor.KIT030}},
		{"the source needs a newer Go than the kit's", func(r *agentRig) {
			e := sourceEntries(agentModule)
			e[0].body = goModText(agentModule, "go 1.25", "github.com/example/dep")
			r.source(agentTag, e...)
		}, []string{"go_version:" + doctor.KIT030}},
		{"the toolchain line wins over the go line", func(r *agentRig) {
			e := sourceEntries(agentModule)
			e[0].body = goModText(agentModule, "go 1.24\ntoolchain go1.24.9", "github.com/example/dep")
			r.source(agentTag, e...)
		}, []string{"go_version:" + doctor.KIT030}},
		{"a path that climbs out of the tree", func(r *agentRig) {
			r.source(agentTag, append(sourceEntries(agentModule), tarEntry{name: "../escape", body: "x"})...)
		}, []string{"vendor:" + doctor.KIT030}},
		{"the source is not a tarball", func(r *agentRig) {
			r.write(r.srcTarball, "plain text")
			r.record("src", filepath.Base(r.srcTarball), sum256([]byte("plain text")))
		}, []string{"vendor:" + doctor.KIT030}},
		{"the compile user does not exist", func(r *agentRig) {
			r.options.LookupUser = func(string) (int, int, error) { return 0, 0, errors.New("no such user") }
		}, []string{"compile:" + doctor.KIT020}},
		{"no setpriv", func(r *agentRig) {
			r.options.Lookup = func(string) (string, error) { return "", execx.ErrNotFound }
		}, []string{"compile:" + doctor.KIT020}},
		{"not enough free disk", func(r *agentRig) {
			r.options.FreeBytes = func(string) (uint64, error) { return 100 << 20, nil }
		}, []string{"disk:" + doctor.KIT014}},
		{"several things at once", func(r *agentRig) {
			_ = os.Remove(r.goTarball)
			r.source(agentTag, sourceEntries(agentModule, "agent/vendor/modules.txt")...)
		}, []string{"toolchain:" + doctor.KIT040, "vendor:" + doctor.KIT030}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentRig(t)
			tc.set(r)
			res, err := r.build(AgentRequest{DryRun: true})
			if res.Report == nil {
				t.Fatalf("no report: %v", err)
			}
			if got := failedAgent(*res.Report); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("failed checks = %v, want %v\n%+v", got, tc.want, res.Report.Checks)
			}
			e := mustAgentErr(t, err)
			wantCode := strings.SplitN(tc.want[0], ":", 2)[1]
			if e.Code != wantCode {
				t.Errorf("code = %s, want %s", e.Code, wantCode)
			}
			if len(r.exec.all()) != 0 {
				t.Errorf("a dry run runs nothing: %v", r.exec.lines())
			}
		})
	}
}

func TestAgentCheckLooksOnEveryRoot(t *testing.T) {
	r := newAgentRig(t)
	second := t.TempDir()
	// The first root has a source nobody recorded, the second a good one.
	if err := os.Remove(filepath.Join(r.kitRoot, "kit", "src", "MANIFEST.json")); err != nil {
		t.Fatal(err)
	}
	first := r.kitRoot
	r.kitRoot = second
	r.source(agentTag, sourceEntries(agentModule)...)
	r.options.KitRoots = func() []string { return []string{first, second} }
	rep, err := r.builder().Check(context.Background(), AgentRequest{})
	if err != nil || !rep.OK() {
		t.Fatalf("%+v %v", rep, err)
	}
}

// The source of this repository itself is complete by the check's own rules (the agent module as the
// release tarball packs it), so the check and the real vendor/ cannot drift apart.
func TestInspectionAcceptsTheRealAgentModule(t *testing.T) {
	root := filepath.Join("..", "..", "..") // agent/
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "agent/" + filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	insp, err := inspectTarball(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := insp.module(); got != "github.com/marinoscar/local-ai/agent" {
		t.Errorf("module = %q", got)
	}
	if p := insp.problems(); len(p) > 0 {
		t.Errorf("the agent module itself is reported incomplete: %v", p)
	}
	if insp.packages == 0 || insp.goNeeds() == "" {
		t.Errorf("packages %d, go needs %q", insp.packages, insp.goNeeds())
	}
}

// ---- failures of the compile -----------------------------------------------------------------

func TestAgentCompileFailures(t *testing.T) {
	tests := []struct {
		name     string
		set      func(r *agentRig)
		wantCode string
		cause    string
	}{
		{"a compile error", func(r *agentRig) { r.goRC, r.goOut = 1, "internal/app/app.go:10:2: undefined: nothing\n" }, doctor.KIT020, "exit"},
		{"go killed", func(r *agentRig) { r.goRC = -1 }, doctor.KIT020, "exit"},
		{"go cannot start", func(r *agentRig) { r.goErr = errors.New("exec format error") }, doctor.KIT020, "exec"},
		{"go times out", func(r *agentRig) { r.goErr = execx.ErrTimeout }, doctor.KIT020, "timeout"},
		{"no program is written", func(r *agentRig) { r.noOutput = true }, doctor.KIT020, "no_output"},
		{"something is not vendored", func(r *agentRig) {
			r.goRC, r.goOut = 1, "main.go:4:2: cannot find module providing package github.com/example/x: module lookup disabled by GOPROXY=off\n"
		}, doctor.KIT030, "not_vendored"},
		{"vendoring is inconsistent", func(r *agentRig) {
			r.goRC, r.goOut = 1, "go: inconsistent vendoring in /var/lib/localai/build/local-ai-node-v0.4.0/agent:\n"
		}, doctor.KIT030, "not_vendored"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentRig(t)
			tc.set(r)
			_, err := r.build(AgentRequest{})
			e := mustAgentErr(t, err)
			if e.Code != tc.wantCode || e.Details["cause"] != tc.cause {
				t.Fatalf("got %s/%v: %v", e.Code, e.Details["cause"], e)
			}
			if tc.cause == "exit" || tc.cause == "not_vendored" {
				if tail, _ := e.Details["log_tail"].(string); tail == "" && r.goOut != "" {
					t.Error("the log tail is not in the error")
				}
			}
			// Nothing was installed, the kit trees are gone, the log stays.
			if r.readBin("aiagent") != string(r.oldBytes) || r.has(r.bin("aiagent.new")) || r.has(r.bin("aiagent.prev")) {
				t.Error("the installed agent was touched")
			}
			if r.restarts != 0 || len(r.audit.events) != 0 {
				t.Errorf("restarts %d audit %v: a failed compile switches nothing", r.restarts, r.audit.events)
			}
			left, _ := os.ReadDir(r.scratch)
			for _, e := range left {
				if e.Name() != "agent-build.log" && e.Name() != ".agent-build.lock" {
					t.Errorf("%s was left in the scratch folder", e.Name())
				}
			}
			if !r.has(filepath.Join(r.scratch, "agent-build.log")) {
				t.Error("no build log")
			}
			// The tree was handed back to root even though the compile failed.
			if r.events.index("chown local-ai-"+agentTag+" 0:0") < 0 {
				t.Errorf("the tree stayed with the compile user: %v", r.events.all())
			}
		})
	}
}

func TestAgentStaticCheckAndSmokeFailuresPutNothingInPlace(t *testing.T) {
	tests := []struct {
		name string
		set  func(r *agentRig)
		want string // a word of the message
	}{
		{"needs shared libraries", func(r *agentRig) { r.elf = elfFixtureNeeded(true, "", "", "libc.so.6") }, "shared libraries"},
		{"not a program", func(r *agentRig) { r.elf = []byte("#!/bin/sh\n") }, "not a program"},
		{"built for another processor", func(r *agentRig) { r.arch = "arm64"; r.toolchain(goVersion) }, "another processor"},
		{"reports another version", func(r *agentRig) { r.newReports = `{"version":"0.0.1","os":"linux","arch":"amd64"}` }, "version"},
		{"reports another architecture", func(r *agentRig) { r.newReports = `{"version":"0.4.0","os":"linux","arch":"arm64"}` }, "architecture"},
		{"reports another system", func(r *agentRig) { r.newReports = `{"version":"0.4.0","os":"darwin","arch":"amd64"}` }, "os"},
		{"exits with an error", func(r *agentRig) { r.newRC = 2 }, "status 2"},
		{"prints something that is not JSON", func(r *agentRig) { r.newReports = "aiagent 0.4.0" }, "JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentRig(t)
			tc.set(r)
			_, err := r.build(AgentRequest{})
			e := mustAgentErr(t, err)
			if e.Code != doctor.KIT021 || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("got %v, want LAI-KIT-021 mentioning %q", e, tc.want)
			}
			if r.readBin("aiagent") != string(r.oldBytes) || r.has(r.bin("aiagent.new")) || r.has(r.bin("aiagent.prev")) {
				t.Error("the installed agent was touched")
			}
			if r.restarts != 0 {
				t.Error("the agent was restarted")
			}
		})
	}
}

// ---- the switch and its rollback ---------------------------------------------------------------

func TestAgentRollsBackWhenTheNewAgentIsNotHealthy(t *testing.T) {
	tests := []struct {
		name   string
		health func(n int) (AgentHealth, error)
		rc     int
		secsOK bool // the 60 s were spent waiting (on the injected clock)
	}{
		{"it never answers", func(n int) (AgentHealth, error) {
			if n == 1 {
				return AgentHealth{Version: oldAgentVer}, nil
			}
			return AgentHealth{}, errors.New("connection refused")
		}, 0, true},
		{"it keeps reporting the old version", func(n int) (AgentHealth, error) { return AgentHealth{Version: oldAgentVer}, nil }, 0, true},
		{"it never stops starting", func(n int) (AgentHealth, error) { return AgentHealth{Version: agentVersion, Starting: n > 1}, nil }, 0, true},
		{"the restart itself fails while the agent was up", func(n int) (AgentHealth, error) { return AgentHealth{Version: oldAgentVer}, nil }, 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentRig(t)
			r.health, r.systemctlRC = tc.health, tc.rc
			start := r.clk.Now()
			res, err := r.build(AgentRequest{})
			e := mustAgentErr(t, err)
			if e.Code != doctor.KIT021 || e.Details["rolled_back"] != true || e.Details["cause"] != "health" {
				t.Fatalf("got %v %v", e, e.Details)
			}
			if res.Switched {
				t.Error("a rolled back build is not a switch")
			}
			if r.readBin("aiagent") != string(r.oldBytes) {
				t.Error("aiagent is not the previous agent again")
			}
			if r.readBin("aiagent.prev") != string(r.oldBytes) {
				t.Error("aiagent.prev must stay")
			}
			if r.has(r.bin("aiagent.new")) {
				t.Error("a staging file was left")
			}
			if r.restarts != 2 {
				t.Errorf("restarts = %d, want one for the new agent and one for the old", r.restarts)
			}
			if waited := r.clk.Now().Sub(start); tc.secsOK && (waited < 58*time.Second || waited > 62*time.Second) {
				t.Errorf("waited %s on the clock, want about 60 s", waited)
			}
			if len(r.audit.events) != 1 || r.audit.events[0].Result != audit.ResultFail || r.audit.events[0].Code != doctor.KIT021 || r.audit.events[0].Target["rolled_back"] != "true" {
				t.Errorf("audit = %+v", r.audit.events)
			}
		})
	}
}

func TestAgentRollbackWithoutAPreviousAgent(t *testing.T) {
	r := newAgentRig(t)
	if err := os.Remove(r.bin("aiagent")); err != nil {
		t.Fatal(err)
	}
	r.health = func(n int) (AgentHealth, error) {
		if n == 1 {
			return AgentHealth{}, errors.New("down") // the unit exists but did not answer before either
		}
		return AgentHealth{}, errors.New("still down")
	}
	// The restart works, so a health check applies even though nothing answered before.
	_, err := r.build(AgentRequest{})
	e := mustAgentErr(t, err)
	if e.Code != doctor.KIT021 || e.Details["rolled_back"] != false || !strings.Contains(e.Message, "no earlier agent") {
		t.Fatalf("got %v %v", e, e.Details)
	}
}

func TestAgentRollbackThatCannotRestoreSaysHowToRecover(t *testing.T) {
	r := newAgentRig(t)
	r.health = func(n int) (AgentHealth, error) {
		if n == 1 {
			return AgentHealth{Version: oldAgentVer}, nil
		}
		if n == 2 {
			_ = os.Remove(r.bin("aiagent.prev")) // the previous agent is lost after the switch
		}
		return AgentHealth{Version: oldAgentVer}, nil
	}
	_, err := r.build(AgentRequest{})
	e := mustAgentErr(t, err)
	if e.Code != doctor.KIT021 || e.Details["rolled_back"] != false || !strings.Contains(e.Message, "sudo cp") {
		t.Fatalf("got %v %v", e, e.Details)
	}
}

func TestAgentHealthIsPolledOnTheInjectedClockOnly(t *testing.T) {
	r := newAgentRig(t)
	r.health = func(n int) (AgentHealth, error) {
		switch {
		case n == 1:
			return AgentHealth{Version: oldAgentVer}, nil
		case n < 5:
			return AgentHealth{}, errors.New("starting up") // the restart takes a few polls
		}
		return AgentHealth{Version: agentVersion}, nil
	}
	start := r.clk.Now()
	res, err := r.build(AgentRequest{})
	if err != nil || !res.Restarted {
		t.Fatalf("%+v %v", res, err)
	}
	if r.clk.nap != 3 || r.clk.Now().Sub(start) != 6*time.Second {
		t.Errorf("slept %d times, %s on the clock", r.clk.nap, r.clk.Now().Sub(start))
	}
}

func TestAgentWithoutAHealthReadSaysItWasNotChecked(t *testing.T) {
	r := newAgentRig(t)
	r.options.Health = nil
	res, err := r.build(AgentRequest{})
	if err != nil || !res.Switched || res.Restarted || len(res.Warnings) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// ---- the lock, cancellation, damaged scratch --------------------------------------------------

func TestAgentBuildRefusesWhileAnotherOneRuns(t *testing.T) {
	r := newAgentRig(t)
	if err := os.MkdirAll(r.scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := lockFile(filepath.Join(r.scratch, agentLockName))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = r.build(AgentRequest{})
	if e := mustAgentErr(t, err); e.Code != doctor.KIT020 || e.Details["cause"] != "locked" {
		t.Fatalf("got %v", err)
	}
}

func TestAgentBuildCancelledLeavesNothingBehind(t *testing.T) {
	r := newAgentRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) == "setpriv" {
			cancel()
			return execx.Result{}, ctx.Err(), true
		}
		return execx.Result{}, nil, false
	})
	b := r.builder()
	_, err := b.Build(ctx, AgentRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if r.readBin("aiagent") != string(r.oldBytes) || r.has(r.bin("aiagent.new")) {
		t.Error("the installed agent was touched")
	}
	left, _ := os.ReadDir(r.scratch)
	for _, e := range left {
		if e.Name() != "agent-build.log" && e.Name() != ".agent-build.lock" {
			t.Errorf("%s was left in the scratch folder", e.Name())
		}
	}
}

func TestAgentExtractionIsHardened(t *testing.T) {
	// A tarball the kit recorded, but with a link that leaves the tree, passes the lookup and is refused
	// when it is unpacked: nothing is written outside and nothing is left.
	r := newAgentRig(t)
	r.source(agentTag, append(sourceEntries(agentModule), tarEntry{name: "agent/link", typ: tar.TypeSymlink, linkname: "/etc/passwd"})...)
	_, err := r.build(AgentRequest{})
	e := mustAgentErr(t, err)
	if e.Code != doctor.KIT001 || e.Details["cause"] != "unsafe_archive" {
		t.Fatalf("got %v %v", e, e.Details)
	}
	if r.exec.ran("go") || r.exec.ran("setpriv") {
		t.Errorf("nothing may be compiled from an unsafe archive: %v", r.exec.lines())
	}
	if r.has(filepath.Join(r.scratch, "local-ai-"+agentTag)) || r.has(filepath.Join(r.scratch, "local-ai-"+agentTag+".tmp")) {
		t.Error("the unsafe tree was left")
	}
}

// A file that is replaced between the lookup and the unpacking is refused: the digest is computed
// again over the bytes that are extracted.
type swapOnOpenFS struct {
	*ownerFS
	path string
	then []byte
	n    int
}

func (f *swapOnOpenFS) Open(name string) (File, error) {
	if name == f.path {
		f.n++
		if f.n == 3 { // the lookup hashed it, the inspection read it: the extraction is the third open
			_ = os.WriteFile(name, f.then, fs.FileMode(0o644))
		}
	}
	return f.ownerFS.Open(name)
}

func TestAgentSourceChangedBetweenCheckAndExtractionIsRefused(t *testing.T) {
	r := newAgentRig(t)
	other := makeTar(t, sourceEntries(agentModule), true)
	other = append(other, 0) // different bytes, still a readable tarball
	r.options.FS = &swapOnOpenFS{ownerFS: r.fsys, path: r.srcTarball, then: other}
	_, err := r.build(AgentRequest{})
	e := mustAgentErr(t, err)
	if e.Code != doctor.KIT001 {
		t.Fatalf("got %v", e)
	}
	if r.exec.ran("go") || r.exec.ran("setpriv") {
		t.Error("a changed source was compiled")
	}
}
