package piper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// installBinary makes <root>/current/piper an executable file, as an install would.
func installBinary(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	cur := filepath.Join(root, "1.0")
	if err := os.MkdirAll(cur, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cur, BinPiper), []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("1.0", filepath.Join(root, CurrentName)); err != nil {
		t.Fatal(err)
	}
	return root
}

func someVoice(t testing.TB) Voice {
	t.Helper()
	copies := installedVoices(t, "piper-en-medium")
	vs := Voices{Catalog: catalogVoices(), Copies: copies}.All()
	if len(vs) != 1 {
		t.Fatal("no voice")
	}
	return vs[0]
}

func TestCmdIsTheContractOfSection432(t *testing.T) {
	root := "/opt/localai/piper"
	rt := Runtime{Root: root, Credential: &execx.Credential{UID: 990, GID: 991}}
	v := Voice{CatalogVoice: CatalogVoice{ID: "piper-en-medium"}, Model: "/ark/speech/piper/piper-en-medium/m.onnx", Config: "/ark/speech/piper/piper-en-medium/m.onnx.json"}
	text := "Press firmly; $(rm -rf /) `id` -- --model /etc/passwd\nsecond line"
	c := rt.Cmd(v, text, 1.05, 1.0)

	if c.Path != "/opt/localai/piper/current/piper" {
		t.Errorf("path = %q", c.Path)
	}
	wantArgs := []string{
		"--model", v.Model, "--config", v.Config,
		"--espeak_data", "/opt/localai/piper/current/espeak-ng-data",
		"--output_raw", "--length_scale", "1.05", "--sentence_silence", "0.25",
	}
	if !reflect.DeepEqual(c.Args, wantArgs) {
		t.Errorf("argv\n got %q\nwant %q", c.Args, wantArgs)
	}
	for _, a := range c.Args {
		if strings.Contains(a, "rm -rf") || strings.Contains(a, "passwd") && a != v.Model && a != v.Config {
			t.Errorf("text reached the argv: %q", a)
		}
	}
	if !reflect.DeepEqual(c.Env, []string{"LD_LIBRARY_PATH=/opt/localai/piper/current"}) {
		t.Errorf("env = %q: the environment must be cleared but for LD_LIBRARY_PATH", c.Env)
	}
	if c.Dir != "/var/lib/localai" {
		t.Errorf("cwd = %q", c.Dir)
	}
	if c.Credential == nil || c.Credential.UID != 990 || c.Credential.GID != 991 {
		t.Errorf("credential = %+v", c.Credential)
	}
	wantLimits := []execx.Rlimit{{Resource: execx.RlimitAS, Limit: 2 << 30}, {Resource: execx.RlimitCPU, Limit: 60}, {Resource: execx.RlimitNOFILE, Limit: 64}}
	if !reflect.DeepEqual(c.Rlimits, wantLimits) {
		t.Errorf("rlimits = %+v, want RLIMIT_AS 2 GiB, RLIMIT_CPU 60 s, RLIMIT_NOFILE 64", c.Rlimits)
	}
	if c.MaxStderr != 4096 {
		t.Errorf("stderr cap = %d, want 4 KiB", c.MaxStderr)
	}
	b := new(strings.Builder)
	buf := make([]byte, 1024)
	for {
		n, err := c.Stdin.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if b.String() != text+"\n" {
		t.Errorf("stdin = %q, want the text and one newline", b.String())
	}
}

func TestCmdDefaultsAndAnUnprivilegedAgent(t *testing.T) {
	c := (Runtime{}).Cmd(Voice{}, "x", 1.05, 1)
	if c.Path != DefaultRoot+"/current/piper" || c.Dir != DefaultWorkDir || c.Credential != nil {
		t.Errorf("defaults: %q %q %+v", c.Path, c.Dir, c.Credential)
	}
	if c := (Runtime{Root: "/x/y/", WorkDir: "/w"}).Cmd(Voice{}, "x", 1, 1); c.Path != "/x/y/current/piper" || c.Dir != "/w" {
		t.Errorf("custom root: %q %q", c.Path, c.Dir)
	}
}

func TestLengthScaleIsDividedBySpeed(t *testing.T) {
	cases := []struct {
		scale, speed float64
		want         string
	}{
		{1.05, 1.0, "1.05"},
		{1.05, 1.5, "0.7"},
		{1.05, 0.7, "1.5"},
		{1.0, 1.0, "1"},
		{1.05, 1.3, "0.808"},
		{1.05, 0, "1.05"}, // a zero speed is not a division by zero
		{1.05, -1, "1.05"},
	}
	for _, c := range cases {
		if got := LengthScaleArg(c.scale, c.speed); got != c.want {
			t.Errorf("LengthScaleArg(%v, %v) = %q, want %q", c.scale, c.speed, got, c.want)
		}
	}
}

func TestSynthTimeoutIsTenSecondsPlusFiftyMsPerCharacter(t *testing.T) {
	cases := map[int]time.Duration{
		0: 10 * time.Second, 1: 10*time.Second + 50*time.Millisecond, 100: 15 * time.Second,
		1000: 60 * time.Second, 2000: 110 * time.Second, 5000: 110 * time.Second, -5: 10 * time.Second,
	}
	for chars, want := range cases {
		if got := SynthTimeout(chars); got != want {
			t.Errorf("SynthTimeout(%d) = %s, want %s", chars, got, want)
		}
	}
	// The timeout of a command counts runes, not bytes.
	rt := Runtime{}
	if got := rt.Cmd(Voice{}, strings.Repeat("ñ", 100), 1, 1).Timeout; got != 15*time.Second {
		t.Errorf("100 runes: timeout %s, want 15s", got)
	}
}

func TestSampleRate(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) Voice {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return Voice{Config: p}
	}
	big := write("big.json", `{"audio":{"sample_rate":22050},"pad":"`+strings.Repeat("x", 2<<20)+`"}`)
	cases := []struct {
		name string
		v    Voice
		want int
		bad  bool
	}{
		{"22050", write("a.json", `{"audio":{"sample_rate":22050},"espeak":{"voice":"en-us"}}`), 22050, false},
		{"16000", write("b.json", `{"audio":{"sample_rate":16000}}`), 16000, false},
		{"missing key", write("c.json", `{"audio":{}}`), 0, true},
		{"zero", write("d.json", `{"audio":{"sample_rate":0}}`), 0, true},
		{"negative", write("e.json", `{"audio":{"sample_rate":-5}}`), 0, true},
		{"too high", write("f.json", `{"audio":{"sample_rate":192000}}`), 0, true},
		{"not json", write("g.json", `not json`), 0, true},
		{"wrong type", write("h.json", `{"audio":{"sample_rate":"22050"}}`), 0, true},
		{"missing file", Voice{Config: filepath.Join(dir, "nope.json")}, 0, true},
		{"over the size bound", big, 0, true},
	}
	for _, c := range cases {
		got, err := (Runtime{}).SampleRate(c.v)
		var se *SynthError
		switch {
		case c.bad && (!errors.As(err, &se) || se.Reason != ReasonBadConfig || se.Code() != doctor.SVC023):
			t.Errorf("%s: err = %v, want a bad_voice_config SynthError with LAI-SVC-023", c.name, err)
		case !c.bad && (err != nil || got != c.want):
			t.Errorf("%s: %d, %v, want %d", c.name, got, err, c.want)
		}
	}
}

func TestCheckBinary(t *testing.T) {
	root := installBinary(t)
	if err := (Runtime{Root: root}).CheckBinary(); err != nil {
		t.Errorf("installed: %v", err)
	}
	for name, mutate := range map[string]func(){
		"not executable": func() { _ = os.Chmod(filepath.Join(root, "1.0", BinPiper), 0o644) },
		"a directory": func() {
			_ = os.Remove(filepath.Join(root, "1.0", BinPiper))
			_ = os.Mkdir(filepath.Join(root, "1.0", BinPiper), 0o755)
		},
		"missing": func() { _ = os.RemoveAll(filepath.Join(root, "1.0")) },
	} {
		mutate()
		err := (Runtime{Root: root}).CheckBinary()
		var se *SynthError
		if !errors.As(err, &se) || se.Reason != ReasonNotInstalled || se.Code() != doctor.SVC020 {
			t.Errorf("%s: err = %v, want LAI-SVC-020", name, err)
		}
		// Put a good one back for the next case.
		_ = os.RemoveAll(filepath.Join(root, "1.0"))
		_ = os.MkdirAll(filepath.Join(root, "1.0"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "1.0", BinPiper), []byte("x"), 0o755)
	}
}

func TestSettleClassifiesHowAProcessEnded(t *testing.T) {
	long := strings.Repeat("e", 5000)
	cases := []struct {
		name    string
		res     execx.StreamResult
		werr    error
		audio   int64
		want    string // reason of the SynthError, "" for nil, "ctx" for the context's error
		wantErr error
	}{
		{"clean", execx.StreamResult{}, nil, 44100, "", nil},
		{"non-zero exit", execx.StreamResult{ExitCode: 3, Stderr: []byte("boom")}, nil, 0, ReasonExit, nil},
		{"non-zero exit after audio", execx.StreamResult{ExitCode: 1}, nil, 9000, ReasonExit, nil},
		{"killed by a signal", execx.StreamResult{ExitCode: -1}, nil, 9000, ReasonExit, nil},
		{"timeout", execx.StreamResult{ExitCode: -1}, execx.ErrTimeout, 100, ReasonTimeout, nil},
		{"empty output", execx.StreamResult{}, nil, 0, ReasonEmpty, nil},
		{"one byte is not a sample", execx.StreamResult{}, nil, 1, ReasonEmpty, nil},
		{"client left", execx.StreamResult{ExitCode: -1}, context.Canceled, 100, "ctx", context.Canceled},
		{"route deadline", execx.StreamResult{ExitCode: -1}, context.DeadlineExceeded, 100, "ctx", context.DeadlineExceeded},
		{"wait failed", execx.StreamResult{}, errors.New("wait: no such process"), 0, ReasonStart, nil},
		{"stderr is sanitized and kept", execx.StreamResult{ExitCode: 2, Stderr: []byte("bad\x00 thing\x07\n" + long)}, nil, 0, ReasonExit, nil},
	}
	for _, c := range cases {
		err := Settle(c.res, c.werr, c.audio)
		var se *SynthError
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: err = %v, want nil", c.name, err)
		case c.want == "ctx" && !errors.Is(err, c.wantErr):
			t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
		case c.want != "" && c.want != "ctx" && (!errors.As(err, &se) || se.Reason != c.want):
			t.Errorf("%s: err = %v, want reason %s", c.name, err, c.want)
		}
		if se != nil && se.Code() != doctor.SVC023 {
			t.Errorf("%s: code %s, want LAI-SVC-023", c.name, se.Code())
		}
		if se != nil && strings.ContainsAny(se.Stderr, "\x00\x07") {
			t.Errorf("%s: stderr keeps control characters: %q", c.name, se.Stderr)
		}
	}
}

func TestSynthesizeCollectsAndValidates(t *testing.T) {
	root := installBinary(t)
	v := someVoice(t)
	pcm := sine(22050, 0.5)
	cases := []struct {
		name   string
		script fake.Script
		want   string // reason, "" for success
		bytes  int
	}{
		{"success", fake.Script{Chunks: [][]byte{pcm[:1000], pcm[1000:]}}, "", len(pcm)},
		{"odd length is trimmed", fake.Script{Chunks: [][]byte{append(append([]byte(nil), pcm...), 0x7f)}}, "", len(pcm)},
		{"exit status", fake.Script{ExitCode: 1, Stderr: "no such voice"}, ReasonExit, 0},
		{"timeout", fake.Script{Chunks: [][]byte{pcm[:100]}, Outcome: fake.Timeout}, ReasonTimeout, 0},
		{"empty", fake.Script{}, ReasonEmpty, 0},
		{"cannot start", fake.Script{StartErr: errors.New("exec format error")}, ReasonStart, 0},
	}
	for _, c := range cases {
		s := fake.NewStreamer(c.script)
		got, rate, err := (Runtime{Root: root, Streamer: s}).Synthesize(context.Background(), v, "Hello.", DefaultConfig(), 1)
		var se *SynthError
		switch {
		case c.want == "" && (err != nil || len(got) != c.bytes || rate != 22050):
			t.Errorf("%s: %d bytes at %d Hz, %v; want %d bytes", c.name, len(got), rate, err, c.bytes)
		case c.want != "" && (!errors.As(err, &se) || se.Reason != c.want):
			t.Errorf("%s: err = %v, want reason %s", c.name, err, c.want)
		}
		for _, p := range s.Procs() {
			if !p.Waited() {
				t.Errorf("%s: the process was never waited for", c.name)
			}
		}
	}
	// A missing binary is LAI-SVC-020 and nothing is started.
	s := fake.NewStreamer(fake.Script{})
	_, _, err := (Runtime{Root: t.TempDir(), Streamer: s}).Synthesize(context.Background(), v, "x", DefaultConfig(), 1)
	var se *SynthError
	if !errors.As(err, &se) || se.Code() != doctor.SVC020 || len(s.Calls()) != 0 {
		t.Errorf("missing binary: err = %v, started %d", err, len(s.Calls()))
	}
}
