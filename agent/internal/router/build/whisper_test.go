package build

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// ---- the fixture: a node that rebuilds whisper.cpp ----------------------------------------

const (
	whisperModel = "/models/ggml-small.bin"
	whisperText  = " The quick brown fox jumps over the lazy dog.\n"
)

// sampleWAV is the stand-in for the embedded drill recording.
var sampleWAV = []byte("RIFF....WAVEfmt sample")

// fakeServices is the agent as the whisper switch sees it.
type fakeServices struct {
	up     bool
	errs   []error // answers of RestartService, in order; nil once exhausted
	calls  []string
	onCall func(n int)
}

func (f *fakeServices) Up(context.Context) bool { return f.up }

func (f *fakeServices) RestartService(_ context.Context, name string) error {
	f.calls = append(f.calls, name)
	if f.onCall != nil {
		f.onCall(len(f.calls))
	}
	var err error
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	return err
}

// whisperEnv is a kit node (kitEnv) whose manager builds whisper.cpp: the root is the whisper root,
// the previous build is in use, every program of the build and of the smoke test is faked.
type whisperEnv struct {
	*kitEnv
	svc *fakeServices

	nodeCommit     string
	whisperBackend string
	llamaBackend   string
	llamaCudaArch  *string

	haveModel bool
	audio     []byte
	// serviceCheck answers the post-switch check; n is the call number (1-based).
	serviceCheck func(n int) error
	checks       int

	cliText  string
	cliRC    int
	helpRC   map[string]int
	programs map[string][]byte // what `cmake --build` writes, by program

	recorded    []string
	sampleSeen  []byte // the bytes of the sample at the time whisper-cli ran
	staleAtConf bool   // the stale file of the scratch tree still existed when cmake configured
	staleFile   string
}

func newWhisperEnv(t *testing.T, opts ...hopt) *whisperEnv {
	t.Helper()
	k := newKitEnv(t, opts...)
	w := &whisperEnv{
		kitEnv: k, svc: &fakeServices{up: true},
		nodeCommit: "0a1b2c3d4" + strings.Repeat("0", 31), whisperBackend: "auto", llamaBackend: BackendCUDA,
		haveModel: true, audio: sampleWAV, cliText: whisperText,
		helpRC: map[string]int{}, programs: map[string][]byte{},
	}
	for _, n := range WhisperRecipe.Programs {
		w.programs[n] = append(elfFixture(true, "", ""), []byte(n)...)
	}
	k.exec.on(w.world)
	k.opts.Recipe = WhisperRecipe
	k.opts.Node = nil
	k.opts.Agent, k.opts.Probe, k.opts.Recorder = nil, nil, nil
	k.opts.Defaults = func() Defaults {
		l := config.LlamaBlock{Backend: w.llamaBackend, CudaArch: w.llamaCudaArch}
		return WhisperDefaults(w.nodeCommit, w.whisperBackend, l)
	}
	k.opts.Services = w.svc
	k.opts.ServiceCheck = func(context.Context) error {
		w.checks++
		if w.serviceCheck != nil {
			return w.serviceCheck(w.checks)
		}
		return nil
	}
	k.opts.SmokeModel = func() (string, bool) { return whisperModel, w.haveModel }
	k.opts.SmokeAudio = func() ([]byte, error) { return w.audio, nil }
	k.opts.RecordCommit = func(c string) error { w.recorded = append(w.recorded, c); return nil }
	w.svc.onCall = func(int) {
		target, _ := os.Readlink(filepath.Join(k.harness.root, "current"))
		k.linkAtRestart = append(k.linkAtRestart, target)
	}
	// A whisper build: the kit alone, offline, activated.
	k.req = Request{FromKit: true, Offline: true, Commit: testSHA}
	return w
}

// world answers whisper's programs. cmake configure is the harness's; cmake --build writes whisper's
// programs instead of llama's.
func (w *whisperEnv) world(c execx.Cmd) (execx.Result, error, bool) {
	res, err, ok := w.answer(c)
	if ok {
		w.events.add("exec %s", line(c))
	}
	return res, err, ok
}

func (w *whisperEnv) answer(c execx.Cmd) (execx.Result, error, bool) {
	switch filepath.Base(c.Path) {
	case "setpriv":
		// The compile runs through setpriv: answer for the program it runs, as the kit rig does for llama's.
		if len(c.Args) > 5 && c.Args[0] == "--reuid" {
			inner := execx.Cmd{Path: c.Args[5], Args: c.Args[6:], Timeout: c.Timeout, MaxOut: c.MaxOut, Env: c.Env}
			if len(inner.Args) > 0 && inner.Args[0] == "--build" {
				if res, err, ok := w.answer(inner); ok {
					w.setprivCmd = append(w.setprivCmd, c)
					return res, err, true
				}
			}
		}
	case "cmake":
		if len(c.Args) > 1 && c.Args[0] == "-S" && w.staleFile != "" {
			if _, err := os.Lstat(w.staleFile); err == nil {
				w.staleAtConf = true
			}
		}
		if len(c.Args) > 1 && c.Args[0] == "--build" {
			if w.buildErr != nil {
				return w.buildRes, w.buildErr, true
			}
			if w.buildRes.ExitCode == 0 && !w.skipBinaries {
				for n, body := range w.programs {
					p := filepath.Join(c.Args[1], "bin", n)
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						return execx.Result{}, err, true
					}
					if err := os.WriteFile(p, body, 0o755); err != nil {
						return execx.Result{}, err, true
					}
				}
			}
			return w.buildRes, nil, true
		}
	case "whisper-server":
		if len(c.Args) == 1 && c.Args[0] == "--help" {
			return execx.Result{ExitCode: w.helpRC["whisper-server"], Stdout: []byte("usage: whisper-server\n")}, nil, true
		}
	case "whisper-cli":
		if len(c.Args) == 1 && c.Args[0] == "--help" {
			return execx.Result{ExitCode: w.helpRC["whisper-cli"], Stdout: []byte("usage: whisper-cli\n")}, nil, true
		}
		for i, a := range c.Args {
			if a == "-f" && i+1 < len(c.Args) {
				w.sampleSeen, _ = os.ReadFile(c.Args[i+1])
			}
		}
		return execx.Result{ExitCode: w.cliRC, Stdout: []byte(w.cliText), Stderr: []byte("whisper_init: ok\n")}, nil, true
	}
	return execx.Result{}, nil, false
}

// tarball puts kit/src/whisper.cpp-<sha>.tar.gz on the kit root and records its digest where the
// kit does ("src" or "mirror"; "" nowhere).
func (w *whisperEnv) tarball(sha string, where, recorded string) string {
	w.t.Helper()
	pre := "whisper.cpp-" + sha + "/"
	data := makeTar(w.t, []tarEntry{
		{name: pre, typ: tar.TypeDir},
		{name: pre + "CMakeLists.txt", body: "project(whisper)\n"},
		{name: pre + "examples/server/server.cpp", body: "int main() {}\n"},
	}, true)
	name := "whisper.cpp-" + sha + ".tar.gz"
	w.write(filepath.Join(w.kitEnv.root, "kit", "src", name), string(data))
	if recorded == "" {
		recorded = sum256(data)
	}
	switch where {
	case "src":
		w.write(filepath.Join(w.kitEnv.root, "kit", "src", "MANIFEST.json"),
			fmt.Sprintf(`{"schema_version":1,"files":[{"kind":"source_tarball","path":%q,"sha256":%q}]}`, name, recorded))
	case "mirror":
		w.write(filepath.Join(w.kitEnv.root, "kit", "git", "whisper.cpp.git", "MANIFEST.json"),
			fmt.Sprintf(`{"schema_version":1,"source":{"type":"git_mirror","tarballs":[{"kind":"source_tarball","path":%q,"sha256":%q}]}}`, name, recorded))
	}
	return filepath.Join(w.kitEnv.root, "kit", "src", name)
}

// mirror makes the (fake) git mirror of whisper.cpp in the kit.
func (w *whisperEnv) mirror(g *kitGit) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Join(w.kitEnv.root, "kit", "git", "whisper.cpp.git"), 0o755); err != nil {
		w.t.Fatal(err)
	}
	w.exec.on(g.handler)
}

func (w *whisperEnv) srcTree(sha string) string {
	return filepath.Join(w.kitEnv.scratch, "whisper.cpp-"+sha)
}

func (w *whisperEnv) root() string { return w.harness.root }

// noLlama fails when a message of a whisper build talks about llama.
func noLlama(t *testing.T, e *Error) {
	t.Helper()
	if strings.Contains(strings.ToLower(e.Message), "llama") {
		t.Errorf("the message of a whisper build mentions llama: %q", e.Message)
	}
}

func readBuildInfo(t *testing.T, path string) BuildInfo {
	t.Helper()
	var info BuildInfo
	if err := json.Unmarshal([]byte(readFile(t, path)), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// ---- the plan: flags per backend, install names -------------------------------------------

func TestWhisperPlanPerBackend(t *testing.T) {
	const jobs = 4
	base := []string{
		"-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=OFF",
		"-DWHISPER_BUILD_EXAMPLES=ON", "-DWHISPER_BUILD_TESTS=OFF", "-DWHISPER_SDL2=OFF", "-DGGML_NATIVE=ON",
	}
	tests := []struct {
		name    string
		in      Inputs
		backend []string
		id      string
	}{
		{"cuda for the RTX 2070 (cc 7.5)", Inputs{Backend: BackendCUDA, CudaArch: "75", Jobs: jobs},
			[]string{"-DGGML_CUDA=ON", "-DCMAKE_CUDA_ARCHITECTURES=75"}, testShort9 + "-cuda"},
		{"cuda for the RTX 4080 (cc 8.9)", Inputs{Backend: BackendCUDA, CudaArch: "89", Jobs: jobs},
			[]string{"-DGGML_CUDA=ON", "-DCMAKE_CUDA_ARCHITECTURES=89"}, testShort9 + "-cuda"},
		{"cuda for two cards", Inputs{Backend: BackendCUDA, CudaArch: "75;89", Jobs: jobs},
			[]string{"-DGGML_CUDA=ON", "-DCMAKE_CUDA_ARCHITECTURES=75;89"}, testShort9 + "-cuda"},
		{"vulkan", Inputs{Backend: BackendVulkan, Jobs: jobs}, []string{"-DGGML_VULKAN=ON"}, testShort9 + "-vulkan"},
		{"cpu has no backend flag and no BLAS", Inputs{Backend: BackendCPU, Jobs: jobs}, nil, testShort9 + "-cpu"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlan(WhisperRecipe, "/opt/localai/whisper.cpp", DefaultScratchDir, tc.in, testSHA)
			wantFlags := append(append([]string{}, base...), tc.backend...)
			if !reflect.DeepEqual(p.CMakeFlags, wantFlags) {
				t.Errorf("flags = %v\nwant    %v", p.CMakeFlags, wantFlags)
			}
			src := "/var/lib/localai/build/whisper.cpp-" + testSHA
			out := "/opt/localai/whisper.cpp/builds/" + tc.id + ".tmp"
			wantConf := append([]string{"-S", src, "-B", out + "/build", "-G", "Ninja"}, wantFlags...)
			if !reflect.DeepEqual(p.ConfigureArgs, wantConf) {
				t.Errorf("configure = %v\nwant       %v", p.ConfigureArgs, wantConf)
			}
			wantBuild := []string{"--build", out + "/build", "--config", "Release", "--target", "whisper-server", "whisper-cli", "-j", "4"}
			if !reflect.DeepEqual(p.BuildArgs, wantBuild) {
				t.Errorf("build = %v\nwant  %v", p.BuildArgs, wantBuild)
			}
			if p.BuildID != tc.id || p.FinalDir != "/opt/localai/whisper.cpp/builds/"+tc.id || p.SourceDir != src {
				t.Errorf("plan = %+v", p)
			}
			for _, f := range p.CMakeFlags {
				if strings.Contains(f, "LLAMA") || strings.Contains(f, "BLAS") {
					t.Errorf("flag %s does not belong to whisper.cpp", f)
				}
			}
		})
	}
}

func TestWhisperPlanWithTheCudaRedistributable(t *testing.T) {
	in := Inputs{Backend: BackendCUDA, CudaArch: "75", Jobs: 2}
	p := newPlan(WhisperRecipe, "/r", "/s", in, testSHA).WithCudaRoot("/s/cuda-12.4")
	joined := strings.Join(p.ConfigureArgs, " ")
	for _, want := range []string{"-DCUDAToolkit_ROOT=/s/cuda-12.4", "-DCMAKE_CUDA_COMPILER=/s/cuda-12.4/bin/nvcc", "-DCMAKE_BUILD_RPATH=$ORIGIN/../lib", "-S /s/whisper.cpp-" + testSHA} {
		if !strings.Contains(joined, want) {
			t.Errorf("configure %q lacks %q", joined, want)
		}
	}
}

// The llama plan is what it was: the generalisation changed nothing for it.
func TestLlamaPlanIsUnchangedByTheRecipe(t *testing.T) {
	in := Inputs{Backend: BackendCUDA, CudaArch: "75", Jobs: 4}
	a := NewPlan("/opt/localai/llama.cpp", in, testSHA)
	b := newPlan(LlamaRecipe, "/opt/localai/llama.cpp", "/anywhere", in, testSHA)
	if !reflect.DeepEqual(a, b) || a.SourceDir != "/opt/localai/llama.cpp/src/"+testSHA {
		t.Errorf("%+v vs %+v", a, b)
	}
	if got := strings.Join(a.BuildArgs, " "); !strings.Contains(got, "--target llama-server llama-cli llama-bench") {
		t.Errorf("build args = %s", got)
	}
}

// ---- inputs: commit, backend, compute capability ------------------------------------------

func TestWhisperResolvesItsInputs(t *testing.T) {
	arch := func(s string) *string { return &s }
	tests := []struct {
		name         string
		req          Request
		commit       string // node.yaml whisper.commit
		wBackend     string // whisper.backend
		lBackend     string // llama.backend
		lArch        *string
		nvidia       string
		wantBackend  string
		wantArch     string
		wantCommit   string
		wantErr      string // a part of the message
		wantCode     string
		wantNvidiaSM bool
	}{
		{name: "auto is the llama backend", wBackend: "auto", lBackend: "vulkan", wantBackend: "vulkan"},
		{name: "an absent whisper.backend means auto", wBackend: "", lBackend: "cpu", wantBackend: "cpu"},
		{name: "no backend anywhere", wBackend: "", lBackend: "", wantErr: "No whisper.cpp backend is set", wantCode: doctor.KIT020},
		{name: "whisper.backend wins over llama's", wBackend: "cpu", lBackend: "cuda", wantBackend: "cpu"},
		{name: "--backend cpu forces the processor", req: Request{Backend: "cpu"}, wBackend: "auto", lBackend: "cuda", wantBackend: "cpu"},
		{name: "--backend auto is llama's backend even when the block says cpu", req: Request{Backend: "auto"}, wBackend: "cpu", lBackend: "vulkan", wantBackend: "vulkan"},
		{name: "--backend cuda over a cpu node", req: Request{Backend: "cuda"}, wBackend: "auto", lBackend: "cpu", wantBackend: "cuda", nvidia: "8.9\n", wantArch: "89", wantNvidiaSM: true},
		{name: "compute capability 7.5 is 75", wBackend: "cuda", lBackend: "cuda", nvidia: "7.5\n", wantBackend: "cuda", wantArch: "75", wantNvidiaSM: true},
		{name: "compute capability 8.9 is 89", wBackend: "cuda", lBackend: "cuda", nvidia: "8.9\n", wantBackend: "cuda", wantArch: "89", wantNvidiaSM: true},
		{name: "two cards are sorted and joined", wBackend: "cuda", lBackend: "cuda", nvidia: "8.9\n7.5\n8.9\n", wantBackend: "cuda", wantArch: "75;89", wantNvidiaSM: true},
		{name: "the llama block's cuda_arch is used without asking nvidia-smi", wBackend: "auto", lBackend: "cuda", lArch: arch("86"), nvidia: "7.5\n", wantBackend: "cuda", wantArch: "86"},
		{name: "--cuda-arch wins", req: Request{CudaArch: "75;86"}, wBackend: "auto", lBackend: "cuda", lArch: arch("89"), wantBackend: "cuda", wantArch: "75;86"},
		{name: "no compute capability is LAI-HW-019", wBackend: "cuda", lBackend: "cuda", nvidia: "N/A\n", wantErr: "could not be detected", wantCode: doctor.HW019},
		{name: "a wrong backend", req: Request{Backend: "gpu"}, wBackend: "auto", lBackend: "cuda", wantErr: "whisper.cpp backend must be", wantCode: doctor.KIT020},
		{name: "the commit of the whisper block is the default", wBackend: "cpu", lBackend: "cpu", wantBackend: "cpu", wantCommit: testSHA},
		{name: "--commit wins over the block", req: Request{Commit: "ABCDEF1"}, wBackend: "cpu", lBackend: "cpu", wantBackend: "cpu", wantCommit: "abcdef1"},
		{name: "no commit anywhere", req: Request{}, commit: "-", wBackend: "cpu", lBackend: "cpu", wantErr: "set whisper.commit in node.yaml", wantCode: doctor.KIT020},
		{name: "a bad commit", req: Request{Commit: "xyz"}, wBackend: "cpu", lBackend: "cpu", wantErr: "7 to 40 lowercase hex", wantCode: doctor.KIT020},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWhisperEnv(t)
			w.whisperBackend, w.llamaBackend, w.llamaCudaArch = tc.wBackend, tc.lBackend, tc.lArch
			switch tc.commit {
			case "":
				w.nodeCommit = testSHA
			case "-":
				w.nodeCommit = ""
			default:
				w.nodeCommit = tc.commit
			}
			if tc.nvidia != "" {
				w.nvidiaOut = tc.nvidia
			}
			before := len(w.exec.all())
			in, err := w.manager().Resolve(context.Background(), tc.req)
			if tc.wantErr != "" {
				e := mustErr(t, err)
				if e.Code != tc.wantCode || !strings.Contains(e.Message, tc.wantErr) {
					t.Fatalf("error = %s: %s, want %s containing %q", e.Code, e.Message, tc.wantCode, tc.wantErr)
				}
				if tc.wantCode != doctor.HW019 { // that message names llama.cuda_arch, where the architecture is kept
					noLlama(t, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if in.Backend != tc.wantBackend || in.CudaArch != tc.wantArch {
				t.Errorf("inputs = %+v, want backend %s arch %q", in, tc.wantBackend, tc.wantArch)
			}
			if tc.wantCommit != "" && in.Commit != tc.wantCommit {
				t.Errorf("commit = %s, want %s", in.Commit, tc.wantCommit)
			}
			if in.Keep != WhisperKeep {
				t.Errorf("keep = %d, want %d (the active build and the one it replaced)", in.Keep, WhisperKeep)
			}
			asked := false
			for _, c := range w.exec.all()[before:] {
				asked = asked || filepath.Base(c.Path) == "nvidia-smi"
			}
			if asked != tc.wantNvidiaSM {
				t.Errorf("nvidia-smi asked = %v, want %v", asked, tc.wantNvidiaSM)
			}
		})
	}
}

// ---- the build, offline from the kit tarball ----------------------------------------------

func TestWhisperBuildsOfflineFromTheKitTarball(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	var progress []Progress
	w.req.Progress = func(p Progress) { progress = append(progress, p) }

	res, err := w.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildID != testID || !res.Switched || res.PreviousBuild != oldID || res.Preflight == nil || !res.Preflight.OK() || res.Preflight.Source != "kit-tarball" {
		t.Fatalf("result = %+v preflight = %+v", res, res.Preflight)
	}
	if w.link() != "builds/"+testID {
		t.Errorf("current = %q", w.link())
	}

	// Configure and build are the plan: whisper's flags, whisper's targets, the tree of the kit
	// tarball in the scratch directory, never the llama ones.
	var configure, compile *execx.Cmd
	for _, c := range w.exec.all() {
		c := c
		if filepath.Base(c.Path) == "cmake" && len(c.Args) > 0 {
			switch c.Args[0] {
			case "-S":
				configure = &c
			case "--build":
				compile = &c
			}
		}
		if n := filepath.Base(c.Path); strings.HasPrefix(n, "llama") || n == "git" {
			t.Errorf("an offline whisper build ran %s", line(c))
		}
	}
	if configure == nil || compile == nil {
		t.Fatalf("no cmake run: %v", w.exec.lines())
	}
	out := filepath.Join(w.root(), "builds", testID+".tmp")
	wantConf := append([]string{"-S", w.srcTree(testSHA), "-B", out + "/build", "-G", "Ninja"}, whisperFlags(Inputs{Backend: BackendCUDA, CudaArch: "75"})...)
	if !reflect.DeepEqual(configure.Args, wantConf) {
		t.Errorf("configure = %v\nwant       %v", configure.Args, wantConf)
	}
	for _, want := range []string{"-DBUILD_SHARED_LIBS=OFF", "-DWHISPER_BUILD_EXAMPLES=ON", "-DWHISPER_SDL2=OFF", "-DGGML_CUDA=ON", "-DCMAKE_CUDA_ARCHITECTURES=75"} {
		if !strings.Contains(strings.Join(configure.Args, " "), want) {
			t.Errorf("configure lacks %s", want)
		}
	}
	if got := strings.Join(compile.Args, " "); !strings.Contains(got, "--target whisper-server whisper-cli -j 5") {
		t.Errorf("build = %s", got)
	}

	// The programs are installed under the build id and the log says what ran.
	for _, n := range WhisperRecipe.Programs {
		fi, err := os.Stat(filepath.Join(w.root(), "builds", testID, "bin", n))
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("%s: %v %v", n, fi, err)
		}
	}
	info := readBuildInfo(t, filepath.Join(w.root(), "builds", testID, "BUILD.json"))
	if info.BuildID != testID || info.Commit != testSHA || info.Backend != BackendCUDA || info.CudaArch != "75" ||
		len(info.SHA256) != 2 || info.SHA256["whisper-server"] == "" || info.SHA256["whisper-cli"] == "" {
		t.Errorf("BUILD.json = %+v", info)
	}
	if want := []string{"static", "help", "transcribe"}; !reflect.DeepEqual(info.Smoke.Checks, want) {
		t.Errorf("smoke checks = %v, want %v", info.Smoke.Checks, want)
	}
	log := readFile(t, filepath.Join(w.root(), "builds", testID, "build.log"))
	if !strings.Contains(log, "-DCMAKE_CUDA_ARCHITECTURES=75") || !strings.Contains(log, "whisper-cli -m "+whisperModel) {
		t.Errorf("build.log lacks the configure line or the smoke line:\n%s", log)
	}

	// The smoke test transcribed the sample, which is a temporary file that is gone again.
	if !reflect.DeepEqual(w.sampleSeen, sampleWAV) {
		t.Errorf("whisper-cli read %q, want the sample recording", w.sampleSeen)
	}
	if !w.exec.ran("whisper-cli -m " + whisperModel + " -f " + filepath.Join(w.scratch, "smoke-"+testID+".wav") + " -l en -nt") {
		t.Errorf("no transcription: %v", w.exec.lines())
	}
	if !w.exec.ran("whisper-server --help") || !w.exec.ran("whisper-cli --help") {
		t.Errorf("--help of both programs must be tested: %v", w.exec.lines())
	}
	left, _ := os.ReadDir(w.scratch)
	if len(left) != 0 {
		t.Errorf("the scratch directory keeps %v (source tree and sample are scratch)", left)
	}

	// Activation: the agent restarted whisper after the link moved, the check ran, the commit was
	// recorded, the switch was audited under its own action, and nothing is left half-built.
	if !reflect.DeepEqual(w.svc.calls, []string{"whisper"}) || !reflect.DeepEqual(w.linkAtRestart, []string{"builds/" + testID}) {
		t.Errorf("restarts = %v with current at %v", w.svc.calls, w.linkAtRestart)
	}
	if w.checks != 1 {
		t.Errorf("service check ran %d times", w.checks)
	}
	if !reflect.DeepEqual(w.recorded, []string{testSHA}) {
		t.Errorf("recorded commits = %v", w.recorded)
	}
	if len(w.audit.events) != 1 || w.audit.events[0].Action != "whisper.build.switch" || w.audit.events[0].Result != audit.ResultOK ||
		w.audit.events[0].Target["build"] != testID || w.audit.events[0].Target["previous"] != oldID {
		t.Errorf("audit = %+v", w.audit.events)
	}
	if w.exists("builds/"+testID+".tmp") || w.exists("builds/"+testID+".tmp.failed") {
		t.Error("a half-built directory was left")
	}
	// Progress talks about whisper.cpp.
	said := false
	for _, p := range progress {
		if strings.Contains(p.Message, "llama") {
			t.Errorf("progress mentions llama: %q", p.Message)
		}
		said = said || strings.Contains(p.Message, "whisper.cpp")
	}
	if !said {
		t.Errorf("progress = %v", progress)
	}
}

func TestWhisperSourceMatrix(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, w *whisperEnv) *kitGit
		code  string // "" = the build succeeds
		via   string // "mirror" or "tarball" when it succeeds
	}{
		{name: "no kit source at all", setup: func(*testing.T, *whisperEnv) *kitGit { return nil }, code: doctor.KIT002},
		{name: "the tarball is of another commit", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			w.tarball(testSHA[:9]+strings.Repeat("0", 31), "src", "")
			return nil
		}, code: doctor.KIT002},
		{name: "a llama tarball of the commit is not a whisper source", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			w.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
			return nil
		}, code: doctor.KIT002},
		{name: "the mirror does not have the commit", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			g := &kitGit{have: false}
			w.mirror(g)
			return g
		}, code: doctor.KIT002},
		{name: "the mirror has it", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			g := &kitGit{have: true, sha: testSHA, archive: treeTar(t)}
			w.mirror(g)
			return g
		}, via: "mirror"},
		{name: "a damaged tarball falls back to the mirror", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			w.tarball(testSHA, "mirror", sum256([]byte("another file")))
			g := &kitGit{have: true, sha: testSHA, archive: treeTar(t)}
			w.mirror(g)
			return g
		}, via: "mirror"},
		{name: "a damaged tarball and no mirror is LAI-KIT-001", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			w.tarball(testSHA, "src", sum256([]byte("another file")))
			return nil
		}, code: doctor.KIT001},
		{name: "a tarball nobody recorded is not trusted", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			w.tarball(testSHA, "", "")
			return nil
		}, code: doctor.KIT001},
		{name: "the tarball wins over the mirror", setup: func(t *testing.T, w *whisperEnv) *kitGit {
			w.tarball(testSHA, "mirror", "")
			g := &kitGit{have: true, sha: testSHA, archive: treeTar(t)}
			w.mirror(g)
			return g
		}, via: "tarball"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWhisperEnv(t)
			g := tc.setup(t, w)
			_, err := w.build()
			if tc.code != "" {
				e := mustErr(t, err)
				if e.Code != tc.code {
					t.Fatalf("code = %s (%v), want %s", e.Code, e, tc.code)
				}
				noLlama(t, e)
				if tc.code == doctor.KIT002 && !strings.Contains(e.Message, "whisper.cpp version") {
					t.Errorf("the missing commit LAI-KIT-002 must name whisper.cpp: %q", e.Message)
				}
				if w.compiled() || len(w.svc.calls) != 0 {
					t.Error("a build or a restart ran without a source")
				}
				if _, serr := os.Stat(w.srcTree(testSHA)); serr == nil {
					t.Error("a failed source left a tree behind")
				}
				if w.link() != "builds/"+oldID {
					t.Errorf("current = %q", w.link())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			archived := g != nil && strings.Contains(strings.Join(g.calls, "\n"), "archive")
			if (tc.via == "mirror") != archived {
				t.Errorf("via %s but git archive ran = %v", tc.via, archived)
			}
		})
	}
}

// The GitHub clone is the one explicit network source; offline it is never tried and the kit's
// answer is LAI-KIT-002.
func TestWhisperOfflineNeverClones(t *testing.T) {
	w := newWhisperEnv(t)
	w.req.FromKit = false
	w.opts.Sources = ChainWithKit(SourceStemWhisper, []string{w.kitEnv.root}, DefaultWhisperCloneURL)
	w.req.Offline = true
	_, err := w.build()
	if e := mustErr(t, err); e.Code != doctor.KIT002 {
		t.Fatalf("got %v", e)
	}
	for _, c := range w.exec.all() {
		if filepath.Base(c.Path) == "git" && strings.Contains(strings.Join(c.Args, " "), "clone") {
			t.Errorf("offline must not clone: %s", line(c))
		}
	}
}

func TestWhisperScratchSourceIsReplacedAndRemoved(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.staleFile = filepath.Join(w.srcTree(testSHA), "stale.txt")
	w.write(w.staleFile, "left by an earlier build")
	w.write(filepath.Join(w.srcTree(testSHA), "CMakeLists.txt"), "project(stale)\n")
	if _, err := w.build(); err != nil {
		t.Fatal(err)
	}
	if w.staleAtConf {
		t.Error("the tree of an earlier build was reused: it must be extracted afresh from the tarball")
	}
	if _, err := os.Stat(w.srcTree(testSHA)); err == nil {
		t.Error("the scratch source tree must be removed after the build")
	}
}

// ---- the preflight and --dry-run ----------------------------------------------------------

func TestWhisperPreflightMatrix(t *testing.T) {
	type env func(w *whisperEnv)
	missing := func(names ...string) env {
		return func(w *whisperEnv) {
			for _, n := range names {
				w.tools[n] = false
			}
		}
	}
	tests := []struct {
		name     string
		backend  string
		env      env
		want     []string
		packages []string
		detail   string
	}{
		{name: "all good on cuda", backend: "cuda"},
		{name: "all good on vulkan", backend: "vulkan"},
		{name: "all good on cpu without any gpu program", backend: "cpu", env: missing("nvcc", "glslc", "nvidia-smi", "vulkaninfo")},
		{name: "cmake missing", backend: "cuda", env: missing("cmake"), want: []string{"tools:" + doctor.KIT010}, packages: []string{"cmake"}},
		{name: "ninja and the compilers missing", backend: "cpu", env: missing("ninja", "gcc", "g++"),
			want: []string{"tools:" + doctor.KIT010}, packages: []string{"build-essential", "ninja-build"}},
		{name: "cmake too old", backend: "cpu", env: func(w *whisperEnv) { w.cmakeOut = "cmake version 3.16.3\n" },
			want: []string{"tools:" + doctor.KIT010}, packages: []string{"cmake"}, detail: "older than 3.21"},
		{name: "nvcc missing", backend: "cuda", env: missing("nvcc"), want: []string{"backend:" + doctor.KIT011}, packages: []string{"nvidia-cuda-toolkit"}},
		{name: "nvcc newer than the driver", backend: "cuda", env: func(w *whisperEnv) { w.driverOut = "CUDA Version: 11.8\n" },
			want: []string{"backend:" + doctor.KIT011}, detail: "newer than the CUDA 11.8"},
		{name: "driver not loaded", backend: "cuda", env: func(w *whisperEnv) { w.driverRC = 9 }, want: []string{"driver:" + doctor.KIT013}},
		{name: "llvmpipe only", backend: "vulkan", env: func(w *whisperEnv) { w.vulkanOut = vulkanSoft },
			want: []string{"backend:" + doctor.KIT012}, detail: "llvmpipe"},
		{name: "disk low in /opt/localai", backend: "cpu", env: func(w *whisperEnv) { w.freeOpt = 3 << 30 }, want: []string{"disk:" + doctor.KIT014}},
		{name: "disk low in the scratch directory", backend: "cpu", env: func(w *whisperEnv) { w.freeScr = 1 << 28 },
			want: []string{"disk:" + doctor.KIT014}, detail: "1 GB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWhisperEnv(t)
			w.tarball(testSHA, "src", "")
			w.req.Backend = tc.backend
			if tc.env != nil {
				tc.env(w)
			}
			rep := w.manager().PreflightReport(context.Background(), w.req)
			got := failedIDs(rep)
			if !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("failed checks = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(rep.Packages, tc.packages) && !(len(rep.Packages) == 0 && len(tc.packages) == 0) {
				t.Errorf("packages = %v, want %v", rep.Packages, tc.packages)
			}
			if len(tc.packages) > 0 && rep.InstallCommand != "sudo aictl kit install-debs --packages "+strings.Join(tc.packages, ",") {
				t.Errorf("install command = %q", rep.InstallCommand)
			}
			if tc.detail != "" {
				found := false
				for _, c := range rep.Failed() {
					found = found || strings.Contains(c.Detail, tc.detail)
				}
				if !found {
					t.Errorf("no failed check mentions %q: %+v", tc.detail, rep.Failed())
				}
			}
			for _, c := range rep.Checks {
				if strings.Contains(strings.ToLower(c.Title+c.Detail), "llama") {
					t.Errorf("check %s talks about llama: %q / %q", c.ID, c.Title, c.Detail)
				}
			}
			if rep.Source != "kit-tarball" {
				t.Errorf("source = %q", rep.Source)
			}
		})
	}
}

func TestWhisperPreflightNeedsTheCommitInTheKit(t *testing.T) {
	w := newWhisperEnv(t)
	rep := w.manager().PreflightReport(context.Background(), w.req)
	if got := failedIDs(rep); !reflect.DeepEqual(got, []string{"source:" + doctor.KIT002}) {
		t.Fatalf("failed = %v", got)
	}
	e := rep.Err()
	if e == nil || e.Code != doctor.KIT002 || !strings.Contains(e.Message, "whisper.cpp version") {
		t.Fatalf("Err() = %v", e)
	}
}

func TestWhisperPreflightWithUnresolvableInputsIsKit020(t *testing.T) {
	w := newWhisperEnv(t)
	w.nodeCommit = ""
	w.req.Commit = ""
	rep := w.manager().PreflightReport(context.Background(), w.req)
	if got := failedIDs(rep); !reflect.DeepEqual(got, []string{"inputs:" + doctor.KIT020}) {
		t.Fatalf("failed = %v", got)
	}
	if !strings.Contains(rep.Checks[0].Detail, "whisper.commit") {
		t.Errorf("detail = %q", rep.Checks[0].Detail)
	}
}

func TestWhisperDryRunPrintsEveryCheckAndChangesNothing(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.req.DryRun = true
	res, err := w.build()
	if err != nil || !res.DryRun || res.Preflight == nil || !res.Preflight.OK() {
		t.Fatalf("%v %+v", err, res)
	}
	var ids []string
	for _, c := range res.Preflight.Checks {
		ids = append(ids, c.ID)
	}
	if want := []string{CheckInputs, CheckDriver, CheckSource, CheckTools, CheckBackend, CheckDisk, CheckJobs}; !reflect.DeepEqual(ids, want) {
		t.Errorf("checks = %v, want %v", ids, want)
	}
	if w.exists(".build.lock") || w.exists("builds/"+testID+".tmp") || len(w.svc.calls) != 0 || w.compiled() || len(w.recorded) != 0 {
		t.Error("a dry run changed the node")
	}
	if left, _ := os.ReadDir(w.scratch); len(left) != 0 {
		t.Errorf("a dry run wrote to the scratch directory: %v", left)
	}
	if w.link() != "builds/"+oldID {
		t.Errorf("current = %q", w.link())
	}

	// Exit 0 only if every check passes: one failure is the error of the dry run, with the report.
	w2 := newWhisperEnv(t)
	w2.tarball(testSHA, "src", "")
	w2.tools["ninja"] = false
	w2.req.DryRun = true
	res, err = w2.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT010 || res.Preflight == nil || res.Preflight.OK() {
		t.Fatalf("%v / %+v", e, res.Preflight)
	}
	if !strings.Contains(e.Message, "sudo aictl kit install-debs --packages ninja-build") {
		t.Errorf("message = %q", e.Message)
	}
}

// Even without --from-kit a whisper build runs the offline preflight (a missing tool is LAI-KIT-010
// with the kit command, never `apt install`).
func TestWhisperAlwaysRunsTheKitPreflight(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.req.FromKit = false
	w.opts.Sources = KitSources(SourceStemWhisper, []string{w.kitEnv.root})
	w.tools["cmake"] = false
	_, err := w.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT010 || strings.Contains(e.Message, "apt install ") || w.compiled() {
		t.Fatalf("got %v", e)
	}
}

// ---- failures: the compile ----------------------------------------------------------------

func TestWhisperCompileFailureIsKit020AndChangesNothing(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.buildRes = execx.Result{ExitCode: 1, Stderr: []byte("error: ggml-cuda.cu: unsupported gpu architecture\n")}
	_, err := w.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT020 || e.Stage != StageBuild {
		t.Fatalf("got %s at %s: %s", e.Code, e.Stage, e.Message)
	}
	noLlama(t, e)
	if !strings.Contains(e.Message, "whisper.cpp") {
		t.Errorf("message = %q", e.Message)
	}
	// The failed tree is kept with its log, current is untouched, nothing restarted or recorded.
	if !w.exists("builds/"+testID+".tmp.failed/build.log") || w.exists("builds/"+testID) {
		t.Error("the failed build must be kept as .tmp.failed")
	}
	if tail, _ := e.Details["log_tail"].(string); !strings.Contains(tail, "unsupported gpu architecture") {
		t.Errorf("log tail = %q", tail)
	}
	if w.link() != "builds/"+oldID || len(w.svc.calls) != 0 || len(w.recorded) != 0 || len(w.audit.events) != 0 {
		t.Errorf("current = %q, restarts %v, recorded %v, audit %v", w.link(), w.svc.calls, w.recorded, w.audit.events)
	}
	if _, serr := os.Stat(w.srcTree(testSHA)); serr == nil {
		t.Error("the scratch source tree must be removed even when the build fails")
	}
}

func TestWhisperMissingProgramIsKit020(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	delete(w.programs, "whisper-server")
	_, err := w.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT020 || e.Details["binary"] != "whisper-server" || w.exists("builds/"+testID) {
		t.Fatalf("got %v %v", e, e.Details)
	}
	noLlama(t, e)
}

// ---- failures: the smoke test keeps the new dir and leaves current alone ------------------

func TestWhisperSmokeFailureLeavesCurrentAndKeepsTheBuildForInspection(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(w *whisperEnv)
		cause  string
		stage  Stage
		inLog  string
		detail string
	}{
		{name: "the transcription exits with an error", setup: func(w *whisperEnv) { w.cliRC = 1 }, cause: "smoke_transcribe_exit", stage: StageSmoke, inLog: "whisper-cli -m"},
		{name: "the transcription prints nothing", setup: func(w *whisperEnv) { w.cliText = "  \n" }, cause: "smoke_transcribe_empty", stage: StageSmoke, inLog: "whisper-cli -m"},
		{name: "whisper-cli --help fails", setup: func(w *whisperEnv) { w.helpRC["whisper-cli"] = 127 }, cause: "smoke_exit", stage: StageSmoke, detail: "whisper-cli --help"},
		{name: "whisper-server --help fails", setup: func(w *whisperEnv) { w.helpRC["whisper-server"] = 2 }, cause: "smoke_exit", stage: StageSmoke, detail: "whisper-server --help"},
		{name: "a libggml shared library is referenced", setup: func(w *whisperEnv) {
			w.programs["whisper-server"] = append(elfFixtureNeeded(true, "", "", "libc.so.6", "libggml-base.so"), []byte("server")...)
		}, cause: "smoke_shared_libs", stage: StageSmoke, detail: "libggml-base.so"},
		{name: "a libwhisper shared library is referenced", setup: func(w *whisperEnv) {
			w.programs["whisper-cli"] = append(elfFixtureNeeded(true, "", "", "libwhisper.so.1"), []byte("cli")...)
		}, cause: "smoke_shared_libs", stage: StageSmoke, detail: "libwhisper.so.1"},
		{name: "a program that is not an ELF file", setup: func(w *whisperEnv) {
			w.programs["whisper-cli"] = []byte("#!/bin/sh\necho hi\n")
		}, cause: "not_elf", stage: StageRunpath},
		{name: "a run path into a home directory", setup: func(w *whisperEnv) {
			w.programs["whisper-cli"] = append(elfFixture(true, "/home/someone/build/lib", ""), []byte("cli")...)
		}, cause: "runpath", stage: StageRunpath},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWhisperEnv(t)
			w.tarball(testSHA, "src", "")
			tc.setup(w)
			_, err := w.build()
			e := mustErr(t, err)
			if e.Code != doctor.KIT021 || e.Stage != tc.stage || e.Details["cause"] != tc.cause {
				t.Fatalf("got %s at %s cause %v: %s", e.Code, e.Stage, e.Details["cause"], e.Message)
			}
			noLlama(t, e)
			if tc.detail != "" && !strings.Contains(e.Message, tc.detail) {
				t.Errorf("message %q lacks %q", e.Message, tc.detail)
			}
			if w.link() != "builds/"+oldID {
				t.Errorf("current moved to %q", w.link())
			}
			if len(w.svc.calls) != 0 || len(w.recorded) != 0 || len(w.audit.events) != 0 {
				t.Errorf("restarts %v, recorded %v, audit %v after a failed smoke test", w.svc.calls, w.recorded, w.audit.events)
			}
			if !w.exists("builds/"+testID+".tmp.failed/build.log") || w.exists("builds/"+testID) || w.exists("builds/"+testID+".tmp") {
				t.Error("the new build must be kept as .tmp.failed and nowhere else")
			}
			if tc.inLog != "" && !strings.Contains(readFile(t, filepath.Join(w.root(), "builds", testID+".tmp.failed", "build.log")), tc.inLog) {
				t.Errorf("build.log lacks %q", tc.inLog)
			}
			if _, serr := os.Stat(filepath.Join(w.scratch, "smoke-"+testID+".wav")); serr == nil {
				t.Error("the sample recording was left in the scratch directory")
			}
		})
	}
}

func TestWhisperSmokeStepsThatCannotRunAreSkippedWithAWarning(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *whisperEnv)
		warn  string
	}{
		{"no verified whisper model", func(w *whisperEnv) { w.haveModel = false }, "no speech model"},
		{"no model function at all", func(w *whisperEnv) { w.opts.SmokeModel = nil }, "no speech model"},
		{"no sample recording", func(w *whisperEnv) { w.opts.SmokeAudio = nil }, "no sample recording"},
		{"an unreadable sample recording", func(w *whisperEnv) {
			w.opts.SmokeAudio = func() ([]byte, error) { return nil, errors.New("embed broken") }
		}, "cannot be read"},
		{"an empty sample recording", func(w *whisperEnv) { w.audio = nil }, "cannot be read"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWhisperEnv(t)
			w.tarball(testSHA, "src", "")
			tc.setup(w)
			res, err := w.build()
			if err != nil {
				t.Fatal(err)
			}
			if !res.Switched {
				t.Errorf("a build whose transcription cannot be tried is still activated: %+v", res)
			}
			found := false
			for _, wn := range res.Warnings {
				found = found || (strings.Contains(wn, tc.warn) && strings.Contains(wn, "--help"))
			}
			if !found {
				t.Errorf("warnings = %v, want one with %q", res.Warnings, tc.warn)
			}
			for _, c := range w.exec.all() {
				if filepath.Base(c.Path) == "whisper-cli" && len(c.Args) > 1 {
					t.Errorf("whisper-cli ran with %v although the step is skipped", c.Args)
				}
			}
			if !w.exec.ran("whisper-cli --help") || !w.exec.ran("whisper-server --help") {
				t.Error("--help is the smoke test when the transcription is skipped")
			}
			info := readBuildInfo(t, filepath.Join(w.root(), "builds", testID, "BUILD.json"))
			if want := []string{"static", "help"}; !reflect.DeepEqual(info.Smoke.Checks, want) {
				t.Errorf("smoke checks = %v, want %v", info.Smoke.Checks, want)
			}
		})
	}
}

// ---- activation: the switch, the check, the rollback --------------------------------------

func TestWhisperActivationFailuresSwitchBack(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(w *whisperEnv)
		cause     string
		wantCalls int // restarts: the new build and the previous one
	}{
		{"the service never answers", func(w *whisperEnv) {
			w.serviceCheck = func(int) error { return errors.New("connection refused") }
		}, "health_timeout", 2},
		{"the restart itself fails", func(w *whisperEnv) {
			w.svc.errs = []error{errors.New("unit failed to start")}
		}, "restart_failed", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWhisperEnv(t)
			w.tarball(testSHA, "src", "")
			tc.setup(w)
			_, err := w.build()
			e := mustErr(t, err)
			if e.Code != doctor.KIT021 || e.Details["cause"] != tc.cause || e.Details["rolled_back"] != true {
				t.Fatalf("got %s cause %v rolled_back %v: %s", e.Code, e.Details["cause"], e.Details["rolled_back"], e.Message)
			}
			noLlama(t, e)
			if !strings.Contains(e.Message, "whisper.cpp build did not work; the previous build is still in use") {
				t.Errorf("message = %q", e.Message)
			}
			if w.link() != "builds/"+oldID {
				t.Errorf("current = %q, want the previous build back", w.link())
			}
			// Whisper is restarted on the new build, then again on the old one, through the agent.
			if !reflect.DeepEqual(w.svc.calls, []string{"whisper", "whisper"}[:tc.wantCalls]) ||
				!reflect.DeepEqual(w.linkAtRestart, []string{"builds/" + testID, "builds/" + oldID}) {
				t.Errorf("restarts = %v, links at restart = %v", w.svc.calls, w.linkAtRestart)
			}
			if len(w.recorded) != 0 {
				t.Errorf("a rolled back build was recorded: %v", w.recorded)
			}
			if len(w.audit.events) != 1 || w.audit.events[0].Action != "whisper.build.switch" || w.audit.events[0].Result != audit.ResultFail || w.audit.events[0].Code != doctor.KIT021 {
				t.Errorf("audit = %+v", w.audit.events)
			}
			// The rejected build stays on disk for inspection, under its own name.
			if !w.exists("builds/" + testID + "/build.log") {
				t.Error("the rejected build must stay in builds/")
			}
		})
	}
}

func TestWhisperServiceCheckWaitsForTheServiceWithoutSleeping(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.serviceCheck = func(n int) error {
		if n < 4 {
			return errors.New("not yet")
		}
		return nil
	}
	res, err := w.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if w.checks != 4 {
		t.Errorf("checks = %d", w.checks)
	}
	if w.clk.nap != 3 || w.clk.slep != 3*2*time.Second {
		t.Errorf("the check slept %d times for %s, want 3 polls of 2 s on the injected clock", w.clk.nap, w.clk.slep)
	}
}

func TestWhisperServiceWithoutConfigurationIsAWarningNotAFailure(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.serviceCheck = func(int) error { return fmt.Errorf("no whisper.env: %w", ErrServiceNotConfigured) }
	res, err := w.build()
	if err != nil || !res.Switched || w.link() != "builds/"+testID {
		t.Fatalf("%v %+v", err, res)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "no configuration yet") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if w.checks != 1 {
		t.Errorf("a service without configuration is not polled: %d checks", w.checks)
	}
}

func TestWhisperWithoutAServiceCheckIsAWarning(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.opts.ServiceCheck = nil
	res, err := w.build()
	if err != nil || !res.Switched || !strings.Contains(strings.Join(res.Warnings, "\n"), "was not checked") {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestWhisperRestartsWithSystemctlWhenTheAgentIsDown(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.svc.up = false
	res, err := w.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if len(w.svc.calls) != 0 {
		t.Errorf("the agent was asked although it is down: %v", w.svc.calls)
	}
	if !w.exec.ran("systemctl restart localai-whisper.service") || w.exec.ran("systemctl restart localai-llama") {
		t.Errorf("commands = %v", w.exec.lines())
	}

	// And when systemctl fails, the switch is undone.
	w2 := newWhisperEnv(t)
	w2.tarball(testSHA, "src", "")
	w2.svc.up = false
	w2.systemctl = execx.Result{ExitCode: 1, Stderr: []byte("Unit not found.")}
	_, err = w2.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT021 || e.Details["rolled_back"] != false || w2.link() != "builds/"+oldID {
		t.Fatalf("got %v (rolled_back %v), current %q", e, e.Details["rolled_back"], w2.link())
	}
}

func TestWhisperBusyAgentIsRetriedThenTheSwitchIsUndone(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	busy := &BusyError{OpID: "op_1"}
	w.svc.errs = []error{busy, busy, busy, busy, busy, busy, busy, busy, busy, busy, busy, busy, busy, busy}
	_, err := w.build()
	e := mustErr(t, err)
	if e.Code != doctor.AGT022 || w.link() != "builds/"+oldID {
		t.Fatalf("got %v, current %q", e, w.link())
	}
	noLlama(t, e)
}

func TestWhisperNoActivateBuildsAndTestsWithoutSwitching(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	w.req.NoActivate = true
	res, err := w.build()
	if err != nil || res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if w.link() != "builds/"+oldID || len(w.svc.calls) != 0 || len(w.recorded) != 0 || len(w.audit.events) != 0 {
		t.Errorf("current %q, restarts %v, recorded %v", w.link(), w.svc.calls, w.recorded)
	}
	if !w.exists("builds/"+testID+"/bin/whisper-server") || w.checks != 0 {
		t.Error("the build is placed and tested, not activated")
	}
}

func TestWhisperRebuildOfTheActiveBuildWithoutActivationIsRefused(t *testing.T) {
	w2 := newWhisperEnv(t)
	w2.tarball(testSHA, "src", "")
	w2.makeBuild(testID, testNow.Add(-time.Hour))
	if err := os.Remove(filepath.Join(w2.root(), "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("builds/"+testID, filepath.Join(w2.root(), "current")); err != nil {
		t.Fatal(err)
	}
	w2.req.NoActivate = true
	_, err := w2.build()
	e := mustErr(t, err)
	if e.Details["cause"] != "active_build" || e.Code != doctor.KIT020 {
		t.Fatalf("got %v", e)
	}
	noLlama(t, e)
}

// ---- retention: the last two builds, never the active one ---------------------------------

func TestWhisperKeepsTheLastTwoBuilds(t *testing.T) {
	tests := []struct {
		name string
		req  func(r *Request)
		kept []string
	}{
		{"by default the active build and the one it replaced", func(*Request) {}, []string{testID, oldID}},
		{"--keep 2 keeps one more", func(r *Request) { r.Keep = 2 }, []string{testID, oldID, "1111aaaa1-cuda"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWhisperEnv(t)
			w.tarball(testSHA, "src", "")
			w.makeBuild("1111aaaa1-cuda", testNow.Add(-72*time.Hour))
			w.makeBuild("0000bbbb2-vulkan", testNow.Add(-96*time.Hour))
			w.write(filepath.Join(w.root(), "builds", "9999cccc3-cuda.tmp.failed", "build.log"), "an old failure")
			tc.req(&w.req)
			res, err := w.build()
			if err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(filepath.Join(w.root(), "builds"))
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			want := append([]string(nil), tc.kept...)
			// The listing is sorted by name.
			sortStrings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("builds = %v, want %v (removed %v)", got, want, res.Removed)
			}
			if w.link() != "builds/"+testID {
				t.Errorf("current = %q", w.link())
			}
		})
	}
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// With --no-activate the build in use and the previous one stay, whatever their age.
func TestWhisperRetentionNeverRemovesTheActiveBuild(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	// The active build is the oldest of all.
	w.makeBuild("1111aaaa1-cuda", testNow.Add(-1*time.Hour))
	w.makeBuild("2222bbbb2-cuda", testNow.Add(-2*time.Hour))
	w.req.NoActivate = true
	if _, err := w.build(); err != nil {
		t.Fatal(err)
	}
	if !w.exists("builds/"+oldID+"/BUILD.json") || w.link() != "builds/"+oldID {
		t.Errorf("the active build %s was removed or current moved to %q", oldID, w.link())
	}
}

// ---- the unprivileged compile is the one of llama -----------------------------------------

func TestWhisperCompilesAsLocalaiAndTheTreeGoesBackToRoot(t *testing.T) {
	w := newWhisperEnv(t)
	w.tarball(testSHA, "src", "")
	fsys := &ownerFS{OSFS: OSFS{}, log: w.events, root: w.harness.root}
	w.opts.FS = fsys
	w.opts.CompileUser = ""
	w.opts.LookupUser = func(name string) (int, int, error) {
		if name != "localai" {
			return 0, 0, fmt.Errorf("unknown user %s", name)
		}
		return 1234, 2345, nil
	}
	res, err := w.build()
	if err != nil {
		t.Fatal(err)
	}
	if len(w.setprivCmd) != 2 {
		t.Fatalf("setpriv ran %d times: %v", len(w.setprivCmd), w.events.all())
	}
	want := []string{"--reuid", "localai", "--regid", "localai", "--clear-groups", "/usr/bin/cmake"}
	for i, c := range w.setprivCmd {
		if !reflect.DeepEqual(c.Args[:6], want) {
			t.Errorf("setpriv argv %d = %v", i, c.Args[:6])
		}
	}
	ev := w.events
	tmp := "builds/" + testID + ".tmp"
	build := ev.index("exec setpriv --reuid localai --regid localai --clear-groups /usr/bin/cmake --build")
	back := ev.index("chown " + tmp + " 0:0")
	backBin := ev.index("chown " + tmp + "/build/bin/whisper-server 0:0")
	rename := ev.index("rename " + tmp + " builds/" + testID)
	smoke := ev.index("exec whisper-server --help")
	if build < 0 || back < 0 || backBin < 0 || rename < 0 || smoke < 0 {
		t.Fatalf("events missing: %v", ev.all())
	}
	if !(build < back && back < backBin && backBin < rename && back < smoke) {
		t.Errorf("wrong order: build %d back %d/%d rename %d smoke %d\n%v", build, back, backBin, rename, smoke, ev.all())
	}
	if !res.Switched {
		t.Errorf("result = %+v", res)
	}
}

// ---- the helpers --------------------------------------------------------------------------

func TestSharedWhisperLibsReadsTheDynamicSection(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name string
		file string
		want []string
		err  bool
	}{
		{"static", write("static", elfFixture(false, "", "")), nil, false},
		{"only system libraries", write("sys", elfFixtureNeeded(true, "", "", "libc.so.6", "libcudart.so.12", "libvulkan.so.1")), nil, false},
		{"ggml and whisper", write("bad", elfFixtureNeeded(true, "", "", "libc.so.6", "libggml.so", "libwhisper.so.1", "libggml-cuda.so")),
			[]string{"libggml.so", "libwhisper.so.1", "libggml-cuda.so"}, false},
		{"not an elf", write("text", []byte("hello")), nil, true},
		{"missing", filepath.Join(dir, "nothing"), nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SharedWhisperLibs(OSFS{}, tc.file)
			if (err != nil) != tc.err || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, %v want %v (error %v)", got, err, tc.want, tc.err)
			}
		})
	}
}

// The recipes keep their words: nothing of the llama manager says whisper, nothing of the whisper
// manager says llama, and the codes are the recipe's own.
func TestTheVoiceOfEachRecipe(t *testing.T) {
	llama := New(Options{Logger: nil})
	if llama.say("Building llama.cpp for llama.backend: sudo aictl rebuild-llama") != "Building llama.cpp for llama.backend: sudo aictl rebuild-llama" {
		t.Error("the llama voice must not rewrite anything")
	}
	if llama.code(CodeBuildFailed) != doctor.LLM018 || llama.code(CodeBuildUnusable) != doctor.LLM019 {
		t.Error("llama keeps LAI-LLM-018 and LAI-LLM-019")
	}
	whisper := New(Options{Recipe: WhisperRecipe})
	if got := whisper.say("llama.cpp: set llama.commit and llama.backend in node.yaml, then run sudo aictl rebuild-llama; llama.cuda_arch stays"); got !=
		"whisper.cpp: set whisper.commit and whisper.backend in node.yaml, then run sudo aictl rebuild-whisper; llama.cuda_arch stays" {
		t.Errorf("say = %q", got)
	}
	if whisper.code(CodeBuildFailed) != doctor.KIT020 || whisper.code(CodeBuildUnusable) != doctor.KIT021 || whisper.code(CodeNoSource) != doctor.KIT002 || whisper.code(CodeSourceDamaged) != doctor.KIT001 {
		t.Error("whisper reports LAI-KIT-020 and LAI-KIT-021 and leaves the shared source codes alone")
	}
	if whisper.opt.Root != DefaultWhisperRoot || llama.opt.Root != DefaultRoot {
		t.Errorf("roots: whisper %s llama %s", whisper.opt.Root, llama.opt.Root)
	}
}
