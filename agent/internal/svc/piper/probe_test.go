package piper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

type probeEnv struct {
	svc      *Service
	streamer *fake.Streamer
	root     string
	cfg      *Config
}

func newProbeEnv(t *testing.T, noBinary bool, voices []string, scripts ...fake.Script) *probeEnv {
	t.Helper()
	root := t.TempDir()
	if !noBinary {
		root = installBinary(t)
	}
	cfg := DefaultConfig()
	st := fake.NewStreamer(scripts...)
	return &probeEnv{
		svc: New(Deps{Config: func() Config { return cfg }, Catalog: catalogVoices(), Copies: installedVoices(t, voices...),
			Streamer: st, Root: root, Limiter: NewLimiter(2, 4)}),
		streamer: st, root: root, cfg: &cfg,
	}
}

func problemCodes(ps []health.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

func TestLiveness(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		noBinary bool
		voices   []string
		wantCode string
	}{
		{"binary and voices", false, bothVoices, ""},
		{"one voice is enough", false, []string{"piper-es-medium"}, ""},
		{"binary missing", true, bothVoices, doctor.SVC020},
		{"no voice", false, nil, doctor.SVC022},
		{"binary missing and no voice names the binary", true, nil, doctor.SVC020},
	}
	for _, c := range cases {
		p := newProbeEnv(t, c.noBinary, c.voices)
		err := p.svc.Probe().Liveness(ctx)
		var ce *health.CodedError
		switch {
		case c.wantCode == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.wantCode != "" && (!errors.As(err, &ce) || ce.Code != c.wantCode):
			t.Errorf("%s: err = %v, want code %s", c.name, err, c.wantCode)
		}
	}
	// An ended context ends the probe.
	p := newProbeEnv(t, false, bothVoices)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := p.svc.Probe().Liveness(cctx); err == nil {
		t.Error("a cancelled context must fail the probe")
	}
}

func TestContent(t *testing.T) {
	ctx := context.Background()
	if c := newProbeEnv(t, false, nil).svc.Probe().Content(ctx); !c.Missing || c.Code != doctor.SVC022 {
		t.Errorf("no voice: %+v, want missing with LAI-SVC-022", c)
	}
	if c := newProbeEnv(t, false, bothVoices).svc.Probe().Content(ctx); c.Missing {
		t.Errorf("voices installed: %+v", c)
	}
}

func TestReadiness(t *testing.T) {
	ctx := context.Background()
	r := newProbeEnv(t, false, bothVoices).svc.Probe().Readiness(ctx)
	if len(r.Problems) != 0 || len(r.Voices) != 2 || r.Voices[0] != "piper-en-medium" || r.Voices[1] != "piper-es-medium" || r.Detail != "2 voices" {
		t.Errorf("both languages: %+v", r)
	}
	r = newProbeEnv(t, false, []string{"piper-en-medium"}).svc.Probe().Readiness(ctx)
	if codes := problemCodes(r.Problems); len(codes) != 1 || codes[0] != doctor.SVC021 || r.Problems[0].Severity != doctor.Warning || r.Detail != "1 voice" {
		t.Errorf("only english: %+v (the missing Spanish voice is LAI-SVC-021, a warning)", r)
	}
	r = newProbeEnv(t, false, nil).svc.Probe().Readiness(ctx)
	if r.Voices == nil || len(r.Voices) != 0 || len(r.Problems) != 2 {
		t.Errorf("none: %+v: voices is an empty list, and both languages are reported", r)
	}
}

func TestDeepCheck(t *testing.T) {
	ctx := context.Background()
	good := func() fake.Script { return fake.Script{Chunks: [][]byte{sine(11025, 0.5)}} } // 0.5 s, about -9 dBFS
	cases := []struct {
		name     string
		noBinary bool
		voices   []string
		scripts  []fake.Script
		want     []string
	}{
		{"both languages pass", false, bothVoices, []fake.Script{good(), good()}, nil},
		{"silence fails", false, bothVoices, []fake.Script{{Chunks: [][]byte{make([]byte, 22050)}}, good()}, []string{doctor.SVC023}},
		{"too quiet fails", false, bothVoices, []fake.Script{good(), {Chunks: [][]byte{sine(11025, 0.005)}}}, []string{doctor.SVC023}},
		{"too short fails", false, bothVoices, []fake.Script{{Chunks: [][]byte{sine(1000, 0.5)}}, good()}, []string{doctor.SVC023}},
		{"too long fails", false, bothVoices, []fake.Script{good(), {Chunks: [][]byte{sine(22050*6, 0.5)}}}, []string{doctor.SVC023}},
		{"exactly at the edges passes", false, bothVoices, []fake.Script{{Chunks: [][]byte{sine(6900, 0.5)}}, {Chunks: [][]byte{sine(22050*5, 0.5)}}}, nil},
		{"piper fails", false, bothVoices, []fake.Script{{ExitCode: 1}, {ExitCode: 1}}, []string{doctor.SVC023}},
		{"binary missing", true, bothVoices, nil, []string{doctor.SVC020}},
		{"no voice", false, nil, nil, []string{doctor.SVC022}},
		{"a language without a voice is left to the readiness check", false, []string{"piper-en-medium"}, []fake.Script{good()}, nil},
	}
	for _, c := range cases {
		p := newProbeEnv(t, c.noBinary, c.voices, c.scripts...)
		got := problemCodes(p.svc.Probe().Deep(ctx))
		if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
			t.Errorf("%s: problems %v, want %v", c.name, got, c.want)
		}
		if run, q := p.svc.limiter.Stats(); run != 0 || q != 0 {
			t.Errorf("%s: slot leaked: %d running, %d queued", c.name, run, q)
		}
	}
}

func TestDeepCheckSpeaksTestAndPruebaWithEachDefaultVoice(t *testing.T) {
	ok := fake.Script{Chunks: [][]byte{sine(11025, 0.5)}}
	p := newProbeEnv(t, false, bothVoices, ok, ok)
	if got := p.svc.Probe().Deep(context.Background()); len(got) != 0 {
		t.Fatalf("problems %v", got)
	}
	procs := p.streamer.Procs()
	if len(procs) != 2 {
		t.Fatalf("%d syntheses", len(procs))
	}
	want := []struct{ text, voice string }{{"Test.\n", "piper-en-medium"}, {"Prueba.\n", "piper-es-medium"}}
	for i, w := range want {
		if got := string(procs[i].Stdin()); got != w.text {
			t.Errorf("synthesis %d spoke %q, want %q", i, got, w.text)
		}
		if m := procs[i].Cmd.Args[1]; filepath.Base(filepath.Dir(m)) != w.voice {
			t.Errorf("synthesis %d used %s, want the %s voice", i, m, w.voice)
		}
	}
}

func TestDeepCheckStopsWithItsContext(t *testing.T) {
	ok := fake.Script{Chunks: [][]byte{sine(11025, 0.5)}}
	p := newProbeEnv(t, false, bothVoices, ok, ok)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := p.svc.Probe().Deep(ctx); len(got) != 0 {
		t.Errorf("a cancelled check reports nothing: %v", got)
	}
	if n := len(p.streamer.Calls()); n != 0 {
		t.Errorf("%d syntheses started on a cancelled context", n)
	}
}

func TestDeepCheckLeavesABusyNodeAlone(t *testing.T) {
	p := newProbeEnv(t, false, bothVoices)
	p.svc.limiter = NewLimiter(1, 0)
	hold, err := p.svc.limiter.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	if got := p.svc.Probe().Deep(context.Background()); len(got) != 0 {
		t.Errorf("a node busy speaking is no finding: %v", got)
	}
	if n := len(p.streamer.Calls()); n != 0 {
		t.Errorf("the check started %d syntheses while the node was busy", n)
	}
}

func TestSpecPlugsIntoTheSupervisor(t *testing.T) {
	p := newProbeEnv(t, false, bothVoices)
	spec := p.svc.Spec()
	if spec.Name != "piper" || spec.Unit != "" || spec.DownCode != doctor.SVC020 || spec.Probe == nil {
		t.Fatalf("spec = %+v", spec)
	}
	if !spec.Enabled() {
		t.Error("enabled by default")
	}
	p.cfg.Enabled = false
	if spec.Enabled() {
		t.Error("piper.enabled: false must disable the service")
	}
	p.cfg.Enabled = true

	clk := cfgfake.NewClock(time.Unix(1_700_000_000, 0))
	sup, err := health.New(health.Options{Specs: []health.Spec{spec}, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sup.Tick(ctx)
	rec := recordOf(t, sup, "piper")
	if rec.State != health.StateOK || rec.Unit != "" || len(rec.Voices) != 2 || len(rec.Problems) != 0 {
		t.Errorf("both voices: %+v", rec)
	}

	// The binary disappears: down at once with LAI-SVC-020 (no unit, so nothing to restart).
	if err := os.RemoveAll(filepath.Join(p.root, "1.0")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(health.DefaultInterval)
	sup.Tick(ctx)
	rec = recordOf(t, sup, "piper")
	if rec.State != health.StateDown || len(rec.Problems) == 0 || rec.Problems[0].Code != doctor.SVC020 {
		t.Errorf("binary gone: %+v", rec)
	}
}

func TestSpecWithNoVoicesIsMissingContent(t *testing.T) {
	p := newProbeEnv(t, false, nil)
	sup, err := health.New(health.Options{Specs: []health.Spec{p.svc.Spec()}, Clock: cfgfake.NewClock(time.Unix(1_700_000_000, 0))})
	if err != nil {
		t.Fatal(err)
	}
	sup.Tick(context.Background())
	rec := recordOf(t, sup, "piper")
	if rec.State != health.StateMissingContent || len(rec.Problems) == 0 || rec.Problems[0].Code != doctor.SVC022 {
		t.Errorf("no voice: %+v", rec)
	}
}

func TestSpecOffIsDisabled(t *testing.T) {
	p := newProbeEnv(t, false, bothVoices)
	p.cfg.Enabled = false
	sup, _ := health.New(health.Options{Specs: []health.Spec{p.svc.Spec()}, Clock: cfgfake.NewClock(time.Unix(1_700_000_000, 0))})
	sup.Tick(context.Background())
	if rec := recordOf(t, sup, "piper"); rec.State != health.StateDisabled {
		t.Errorf("piper.enabled false: %+v", rec)
	}
}

func recordOf(t *testing.T, sup *health.Supervisor, name string) health.Record {
	t.Helper()
	for _, r := range sup.Records(context.Background()) {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no record for %s", name)
	return health.Record{}
}

var _ config.Clock = (*cfgfake.Clock)(nil)
