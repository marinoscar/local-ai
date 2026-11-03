package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// TestAgentRealBuild rebuilds the agent for real, the way `aictl rebuild-agent` does, from a source
// tarball of the release layout (agent/ with go.mod, cmd/aiagent and vendor/, no top-level folder): the
// CI job `agent-rebuild` (.github/workflows/agent-rebuild.yml) runs it inside a network namespace
// (`unshare -n`), and an operator can run it by hand. It is skipped unless LOCALAI_AGENT_KIT_TARBALL
// names a file called local-ai-node-v<semver>-src.tar.gz (scripts/release/build-src-tarball.sh makes
// one; `tar -czf <name> -C <repository> agent` is enough for a try):
//
//	LOCALAI_AGENT_KIT_TARBALL   the source tarball (required)
//	LOCALAI_AGENT_GO            the go program the kit's "toolchain" runs (default: go on PATH)
//	LOCALAI_EXPECT_NO_NETWORK=1 fail unless the network really is unreachable
//
// The kit's toolchain is a stand-in that runs that go (GOTOOLCHAIN=local, so it is the Go that builds),
// the compile and the new agent's `version --json` are real, and nothing outside the temporary
// directories is touched: the installed agent is a script, systemctl is `true`, the agent's health is
// answered by the test. Two things are proved: the build reports the tag's version and is swapped in
// with the previous agent kept, and a new agent that never becomes healthy is rolled back.
func TestAgentRealBuild(t *testing.T) {
	tarball := os.Getenv("LOCALAI_AGENT_KIT_TARBALL")
	if tarball == "" {
		t.Skip("set LOCALAI_AGENT_KIT_TARBALL to rebuild the agent for real (see the comment of this test)")
	}
	m := regexp.MustCompile(`^local-ai-(node-v([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?))-src\.tar\.gz$`).FindStringSubmatch(filepath.Base(tarball))
	if m == nil {
		t.Fatalf("the tarball %q is not named local-ai-node-v<semver>-src.tar.gz", tarball)
	}
	tag, version := m[1], m[2]
	if os.Getenv("LOCALAI_EXPECT_NO_NETWORK") == "1" {
		if c, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second); err == nil {
			_ = c.Close()
			t.Fatal("the network is reachable: this build must run with it disabled")
		}
	}
	goBin := os.Getenv("LOCALAI_AGENT_GO")
	if goBin == "" {
		p, err := exec.LookPath("go")
		if err != nil {
			t.Fatal("no go program: set LOCALAI_AGENT_GO")
		}
		goBin = p
	}
	goBin, err := filepath.Abs(goBin)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(goBin, "env", "GOVERSION").Output()
	if err != nil {
		t.Fatalf("go env GOVERSION: %v", err)
	}
	goVer := strings.TrimPrefix(strings.TrimSpace(string(out)), "go")
	arch := kitArch(runtime.GOARCH)

	// The kit: the source tarball as it is, and a toolchain tarball whose go is the real one.
	kitRoot := t.TempDir()
	data, err := os.ReadFile(tarball)
	if err != nil {
		t.Fatal(err)
	}
	put := func(part, name string, body []byte) {
		if err := os.MkdirAll(filepath.Join(kitRoot, "kit", part), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(kitRoot, "kit", part, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("src", filepath.Base(tarball), data)
	put("src", "MANIFEST.json", []byte(fmt.Sprintf(`{"schema_version":1,"files":[{"kind":"source_tarball","path":%q,"sha256":%q}]}`, filepath.Base(tarball), sum256(data))))
	var tc bytes.Buffer
	gz := gzip.NewWriter(&tc)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\nexec " + goBin + " \"$@\"\n"
	for _, e := range []struct {
		name string
		body string
		mode int64
		dir  bool
	}{{"go/", "", 0o755, true}, {"go/bin/", "", 0o755, true}, {"go/bin/go", script, 0o755, false}} {
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.dir {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	tcName := "go" + goVer + ".linux-" + arch + ".tar.gz"
	put("toolchains", tcName, tc.Bytes())
	put("toolchains", "MANIFEST.json", []byte(fmt.Sprintf(`{"schema_version":1,"files":[{"kind":"toolchain","path":%q,"sha256":%q}]}`, tcName, sum256(tc.Bytes()))))

	// The node: an installed agent that is a script, and every program of the build real except systemctl.
	installed := []byte(fmt.Sprintf("#!/bin/sh\necho '{\"version\":\"0.0.1\",\"os\":\"linux\",\"arch\":%q}'\n", arch))
	run := func(t *testing.T, healthy bool) (*AgentBuilder, string, *manualClock, *int) {
		t.Helper()
		opt := t.TempDir()
		if err := os.MkdirAll(filepath.Join(opt, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(opt, "bin", "aiagent"), installed, 0o755); err != nil {
			t.Fatal(err)
		}
		clk := &manualClock{t: testNow}
		reads := new(int)
		return NewAgentBuilder(AgentOptions{
			KitRoots: func() []string { return []string{kitRoot} },
			Exec:     systemctlIsTrue{execx.Exec{}},
			Lookup: func(name string) (string, error) {
				if name == "systemctl" {
					return "/systemctl-stand-in", nil
				}
				return execx.Lookup(name)
			},
			ScratchDir: filepath.Join(t.TempDir(), "build"), OptRoot: opt, Arch: arch, CompileUser: CompileAsCaller,
			Health: func(context.Context) (AgentHealth, error) {
				*reads++
				if *reads == 1 {
					return AgentHealth{Version: "0.0.1"}, nil // the agent that is running before the switch
				}
				if healthy {
					return AgentHealth{Version: version}, nil
				}
				return AgentHealth{}, errors.New("connection refused")
			},
			Clock: clk.Now, Sleep: clk.Sleep,
		}), opt, clk, reads
	}

	t.Run("builds, reports the tag's version and swaps it in", func(t *testing.T) {
		b, opt, _, _ := run(t, true)
		res, err := b.Build(context.Background(), AgentRequest{Tag: tag})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Switched || !res.Restarted || res.Version != version || res.Previous != "0.0.1" {
			t.Fatalf("result = %+v", res)
		}
		// The installed agent is the compiled program and says the version of the tag.
		raw, err := os.ReadFile(filepath.Join(opt, "bin", "aiagent"))
		if err != nil || !bytes.HasPrefix(raw, []byte("\x7fELF")) {
			t.Fatalf("aiagent is not a compiled program: %v", err)
		}
		vout, err := exec.Command(filepath.Join(opt, "bin", "aiagent"), "version", "--json").Output()
		if err != nil {
			t.Fatal(err)
		}
		var info struct{ Version, OS, Arch string }
		if err := json.Unmarshal(vout, &info); err != nil || info.Version != version || info.OS != "linux" || info.Arch != arch {
			t.Fatalf("version --json = %s (%v), want %s", vout, err, version)
		}
		if got, _ := os.ReadFile(filepath.Join(opt, "bin", "aiagent.prev")); !bytes.Equal(got, installed) {
			t.Error("aiagent.prev is not the agent that was installed")
		}
		if _, err := os.Stat(filepath.Join(opt, "bin", "aiagent.new")); err == nil {
			t.Error("aiagent.new was left behind")
		}
		if left, _ := os.ReadDir(filepath.Dir(res.LogPath)); len(left) != 2 {
			t.Errorf("the scratch folder keeps %d entries, want the log and the lock only", len(left))
		}
	})

	t.Run("an agent that never becomes healthy is rolled back", func(t *testing.T) {
		b, opt, clk, _ := run(t, false)
		start := clk.Now()
		_, err := b.Build(context.Background(), AgentRequest{Tag: tag})
		var e *Error
		if !errors.As(err, &e) || e.Code != doctor.KIT021 || e.Details["rolled_back"] != true {
			t.Fatalf("err = %v", err)
		}
		if got, _ := os.ReadFile(filepath.Join(opt, "bin", "aiagent")); !bytes.Equal(got, installed) {
			t.Error("the previous agent was not put back")
		}
		if waited := clk.Now().Sub(start); waited < 58*time.Second || waited > 62*time.Second {
			t.Errorf("waited %s, want about 60 s", waited)
		}
	})

	t.Run("the dry run says the source is complete", func(t *testing.T) {
		b, _, _, _ := run(t, true)
		rep, err := b.Check(context.Background(), AgentRequest{Tag: tag, DryRun: true})
		if err != nil || !rep.OK() || rep.Version != version || rep.GoVersion != goVer {
			t.Fatalf("%+v %v", rep, err)
		}
	})
}

// systemctlIsTrue runs every program as it is, except the stand-in for systemctl, which does nothing.
type systemctlIsTrue struct{ execx.Exec }

func (s systemctlIsTrue) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if c.Path == "/systemctl-stand-in" {
		return execx.Result{}, nil
	}
	return s.Exec.Run(ctx, c)
}
