package build

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// kitEnv is the harness of a node with a kit: a content root with the kit directory, the source tree
// not extracted yet, every program of the offline preflight faked, and the request a --from-kit
// build without activation.
type kitEnv struct {
	*harness
	root    string // the kit's content root
	scratch string

	// What the preflight's programs answer.
	driverOut  string // `nvidia-smi` with no arguments
	driverRC   int
	cmakeOut   string
	nvccOut    string
	pkgConfRC  int
	vulkanOut  string
	freeOpt    uint64
	freeScr    uint64
	events     *eventLog
	setprivCmd []execx.Cmd
}

// eventLog is an ordered log shared by the exec fake and the filesystem fake.
type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ev = append(l.ev, fmt.Sprintf(format, a...))
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ev...)
}

func (l *eventLog) index(prefix string) int {
	for i, e := range l.all() {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

const (
	driverHeader = "| NVIDIA-SMI 570.00   Driver Version: 570.00   CUDA Version: 12.8     |\n"
	cmakeNew     = "cmake version 3.28.3\n\nCMake suite maintained and supported by Kitware (kitware.com/cmake).\n"
	vulkanReal   = "Devices:\n========\nGPU0:\n\tapiVersion = 1.3.277\n\tdeviceName = Test Graphics 9000\n\tdeviceType = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU\n"
	vulkanSoft   = "Devices:\n========\nGPU0:\n\tapiVersion = 1.3.255\n\tdeviceName = llvmpipe (LLVM 17.0.6, 256 bits)\n\tdeviceType = PHYSICAL_DEVICE_TYPE_CPU\n"
)

// newKitEnv builds the harness. The preflight of a --from-kit build passes unless a test breaks a
// part of the node.
func newKitEnv(t *testing.T, opts ...hopt) *kitEnv {
	t.Helper()
	h := newHarness(t, append([]hopt{withoutSource()}, opts...)...)
	k := &kitEnv{
		harness: h, root: t.TempDir(), scratch: t.TempDir(), events: &eventLog{},
		driverOut: driverHeader, cmakeOut: cmakeNew,
		nvccOut:   "nvcc: NVIDIA (R) Cuda compiler driver\nCuda compilation tools, release 12.4, V12.4.131\n",
		vulkanOut: vulkanReal, freeOpt: 50 << 30, freeScr: 50 << 30,
	}
	for _, n := range []string{"gcc", "g++", "pkg-config", "setpriv", "xz", "vulkaninfo"} {
		h.tools[n] = true
	}
	h.req = Request{Backend: BackendCUDA, FromKit: true, Offline: true, NoActivate: true}
	h.opts.KitRoots = func() []string { return []string{k.root} }
	h.opts.ScratchDir = k.scratch
	h.opts.Arch = "amd64"
	h.opts.FreeBytes = func(p string) (uint64, error) {
		if strings.HasPrefix(p, k.scratch) {
			return k.freeScr, nil
		}
		return k.freeOpt, nil
	}
	h.exec.on(k.world)
	return k
}

// world answers the programs the offline preflight adds to a build's.
func (k *kitEnv) world(c execx.Cmd) (execx.Result, error, bool) {
	base := filepath.Base(c.Path)
	k.events.add("exec %s", line(c))
	switch base {
	case "nvidia-smi":
		if len(c.Args) == 0 {
			if k.driverRC != 0 {
				return exitWith(k.driverRC, "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver."), nil, true
			}
			return okOut(k.driverOut), nil, true
		}
	case "cmake":
		if len(c.Args) == 1 && c.Args[0] == "--version" {
			return okOut(k.cmakeOut), nil, true
		}
	case "nvcc":
		return okOut(k.nvccOut), nil, true
	case "pkg-config":
		return execx.Result{ExitCode: k.pkgConfRC}, nil, true
	case "vulkaninfo":
		return okOut(k.vulkanOut), nil, true
	case "setpriv":
		k.setprivCmd = append(k.setprivCmd, c)
		if len(c.Args) > 5 && c.Args[0] == "--reuid" {
			inner := execx.Cmd{Path: c.Args[5], Args: c.Args[6:], Timeout: c.Timeout, MaxOut: c.MaxOut, Env: c.Env}
			res, err, ok := k.harness.world(inner)
			if ok {
				return res, err, true
			}
			return execx.Result{}, fmt.Errorf("fake: unexpected command under setpriv: %s", line(inner)), true
		}
	case "git":
		return execx.Result{}, nil, false
	}
	return execx.Result{}, nil, false
}

// ---- kit content -------------------------------------------------------------------------

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// kitTree is the content of the source tarball of a commit as the git_mirror resolver writes it:
// one top-level directory, <stem>-<commit>/.
func kitTree(t *testing.T, sha string) []byte {
	t.Helper()
	pre := "llama.cpp-" + sha + "/"
	return makeTar(t, []tarEntry{
		{name: pre, typ: tar.TypeDir},
		{name: pre + "CMakeLists.txt", body: "project(llama)\n"},
		{name: pre + "ggml/", typ: tar.TypeDir},
		{name: pre + "ggml/src/ggml.c", body: "int x;\n"},
		{name: pre + "scripts/run.sh", body: "#!/bin/sh\n", mode: 0o755},
	}, true)
}

// kitTarball writes kit/src/llama.cpp-<sha>.tar.gz on the kit root and records its digest where the
// kit does: "src" in kit/src/MANIFEST.json, "mirror" in the mirror item's MANIFEST (source.tarballs),
// "" nowhere. recorded is the digest to record ("" for the file's own).
func (k *kitEnv) kitTarball(sha string, data []byte, where, recorded string) string {
	k.t.Helper()
	name := "llama.cpp-" + sha + ".tar.gz"
	k.write(filepath.Join(k.root, "kit", "src", name), string(data))
	if recorded == "" {
		recorded = sum256(data)
	}
	switch where {
	case "src":
		k.write(filepath.Join(k.root, "kit", "src", "MANIFEST.json"),
			fmt.Sprintf(`{"schema_version":1,"files":[{"kind":"source_tarball","path":%q,"sha256":%q}]}`, name, recorded))
	case "mirror":
		k.write(filepath.Join(k.root, "kit", "git", "llama.cpp.git", "MANIFEST.json"),
			fmt.Sprintf(`{"schema_version":1,"source":{"type":"git_mirror","tarballs":[{"kind":"source_tarball","path":%q,"sha256":%q}]}}`, name, recorded))
	}
	return filepath.Join(k.root, "kit", "src", name)
}

// kitMirror makes the (fake) git mirror of the kit; the fake git answers for it through g.
func (k *kitEnv) kitMirror(g *kitGit) string {
	k.t.Helper()
	dir := filepath.Join(k.root, "kit", "git", "llama.cpp.git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		k.t.Fatal(err)
	}
	k.exec.on(g.handler)
	return dir
}

// kitGit answers `git cat-file -e`, `rev-parse` and `archive` of the kit mirror.
type kitGit struct {
	have    bool
	sha     string
	archive []byte
	calls   []string
}

func (g *kitGit) handler(c execx.Cmd) (execx.Result, error, bool) {
	if filepath.Base(c.Path) != "git" {
		return execx.Result{}, nil, false
	}
	g.calls = append(g.calls, strings.Join(c.Args, " "))
	_, sub, rest := gitSub(c.Args)
	switch sub {
	case "cat-file":
		if g.have {
			return execx.Result{}, nil, true
		}
		return execx.Result{ExitCode: 128, Stderr: []byte("fatal: Not a valid object name")}, nil, true
	case "rev-parse":
		if g.have {
			return okOut(g.sha + "\n"), nil, true
		}
		return execx.Result{ExitCode: 1}, nil, true
	case "archive":
		out := ""
		for i, a := range rest {
			if a == "-o" {
				out = rest[i+1]
			}
		}
		if err := os.WriteFile(out, g.archive, 0o644); err != nil {
			return execx.Result{}, err, true
		}
		return execx.Result{}, nil, true
	}
	return execx.Result{}, nil, false
}

func (k *kitEnv) srcTree() string { return filepath.Join(k.harness.root, "src", testSHA) }

// compiled reports whether configure or build ran (cmake --version is only a version check).
func (k *kitEnv) compiled() bool {
	for _, c := range k.exec.all() {
		if filepath.Base(c.Path) == "cmake" && len(c.Args) > 0 && (c.Args[0] == "-S" || c.Args[0] == "--build") {
			return true
		}
	}
	return false
}

func failedIDs(r Report) []string {
	var out []string
	for _, c := range r.Failed() {
		out = append(out, c.ID+":"+c.Code)
	}
	return out
}
