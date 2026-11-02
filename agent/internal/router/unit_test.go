package router

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// execStartArgv reads the ExecStart of a unit the way systemd does (systemd.service, "Command
// lines"): a trailing backslash joins the next line, words are split at white space, single and
// double quotes group, a backslash escapes, and %% and $$ are literal. %d and the like stay as
// written: they are the specifiers the unit means.
func execStartArgv(t *testing.T, unit string) []string {
	t.Helper()
	var joined string
	lines := strings.Split(unit, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "ExecStart=") {
			continue
		}
		cur := strings.TrimPrefix(lines[i], "ExecStart=")
		for strings.HasSuffix(cur, `\`) && i+1 < len(lines) {
			i++
			cur = strings.TrimSuffix(cur, `\`) + " " + lines[i]
		}
		joined = cur
		break
	}
	if joined == "" {
		t.Fatalf("no ExecStart in\n%s", unit)
	}
	var argv []string
	var word strings.Builder
	have := false
	var quote rune
	rs := []rune(joined)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			have = true
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, have = r, true
		case quote == 0 && (r == ' ' || r == '\t'):
			if have {
				argv = append(argv, word.String())
				word.Reset()
				have = false
			}
		case (r == '%' || r == '$') && i+1 < len(rs) && rs[i+1] == r:
			word.WriteRune(r)
			have = true
			i++
		default:
			word.WriteRune(r)
			have = true
		}
	}
	if quote != 0 {
		t.Fatalf("unterminated quote in %q", joined)
	}
	if have {
		argv = append(argv, word.String())
	}
	return argv
}

func mustRender(t *testing.T, in UnitInputs) string {
	t.Helper()
	r, err := RenderUnit(in)
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}
	return string(r.Data)
}

// singleN3 is the ai-n3 default model as single mode serves it.
func singleN3() *SingleModel {
	ms := aiN3Models()
	return &SingleModel{Model: &ms[0], Root: rootInternal, Aliases: []string{"qwen3.5-9b"}}
}

func TestUnitGoldenRouterAiN3(t *testing.T) {
	got := mustRender(t, UnitInputs{Router: routerBlock(1, 512)})
	checkGolden(t, "localai-llama.router.service", []byte(got))
}

func TestUnitGoldenRouterAiN1(t *testing.T) {
	got := mustRender(t, UnitInputs{Router: routerBlock(3, 2048)})
	checkGolden(t, "localai-llama.router.ai-n1.service", []byte(got))
	if !strings.Contains(got, "  --models-max 3 \\\n  --models-memory-margin 2048 \\\n") {
		t.Fatalf("ai-n1 arguments:\n%s", got)
	}
}

func TestUnitGoldenSingleAiN3(t *testing.T) {
	got := mustRender(t, UnitInputs{Mode: LlamaModeSingle, Single: singleN3()})
	checkGolden(t, "localai-llama.single.service", []byte(got))
	want := []string{
		"/opt/localai/llama.cpp/current/bin/llama-server",
		"-m", "/srv/localai/models/qwen3.5-9b-q4km/Qwen3.5-9B-Q4_K_M.gguf",
		"--alias", "qwen3.5-9b-q4km,qwen3.5-9b",
		"--tags", "chat,reasoning,coder",
		"-c", "32768", "-ngl", "99", "--flash-attn", "on", "--parallel", "1",
		"--spec-type", "draft-mtp", "--spec-draft-n-max", "3",
		"--host", "127.0.0.1", "--port", "8080", "--api-key-file", "%d/llama-api-key",
	}
	if g := execStartArgv(t, got); !reflect.DeepEqual(g, want) {
		t.Fatalf("argv\n got %q\nwant %q", g, want)
	}
}

// TestUnitSingleDiffersOnlyInExecStart: section 3.4 says the single unit has the same [Unit],
// [Install] and hardening as the router unit.
func TestUnitSingleDiffersOnlyInExecStart(t *testing.T) {
	strip := func(s string) string {
		var out []string
		skip := false
		for _, l := range strings.Split(s, "\n") {
			switch {
			case strings.HasPrefix(l, "ExecStart="):
				skip = true
			case skip && strings.HasPrefix(l, "  "):
			default:
				skip = false
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	}
	r := mustRender(t, UnitInputs{Router: routerBlock(1, 512)})
	s := mustRender(t, UnitInputs{Mode: LlamaModeSingle, Single: singleN3()})
	if strip(r) != strip(s) {
		t.Fatalf("units differ outside ExecStart:\n%s\n---\n%s", strip(r), strip(s))
	}
}

func TestUnitRouterRules(t *testing.T) {
	t.Run("a zero margin omits the flag", func(t *testing.T) {
		got := mustRender(t, UnitInputs{Router: routerBlock(2, 0)})
		if strings.Contains(got, "memory-margin") {
			t.Fatalf("margin 0 must not render the flag:\n%s", got)
		}
		if !strings.Contains(got, "--models-max 2 \\\n  --no-models-autoload") {
			t.Fatalf("arguments:\n%s", got)
		}
	})
	t.Run("a negative margin omits it too", func(t *testing.T) {
		if strings.Contains(mustRender(t, UnitInputs{Router: routerBlock(1, -5)}), "memory-margin") {
			t.Fatal("negative margin rendered")
		}
	})
	t.Run("models_max below 1 counts as 1, as in the fit rules", func(t *testing.T) {
		for _, n := range []int{0, -3} {
			if !strings.Contains(mustRender(t, UnitInputs{Router: routerBlock(n, 512)}), "--models-max 1 \\\n") {
				t.Fatalf("models_max %d", n)
			}
		}
	})
	t.Run("no per-model argument on the router command line", func(t *testing.T) {
		argv := execStartArgv(t, mustRender(t, UnitInputs{Router: routerBlock(3, 2048)}))
		for _, a := range argv {
			if a == "-m" || a == "--model" || a == "-c" || a == "--alias" || a == "--tags" || a == "-ngl" {
				t.Fatalf("per-model argument %q in router mode: %q", a, argv)
			}
		}
		// autoload stays off.
		found := false
		for _, a := range argv {
			found = found || a == "--no-models-autoload"
		}
		if !found {
			t.Fatal("--no-models-autoload missing")
		}
	})
	t.Run("the port comes from node.yaml", func(t *testing.T) {
		got := mustRender(t, UnitInputs{Router: routerBlock(1, 0), Port: 18080})
		if !strings.Contains(got, "  --port 18080 \\\n") {
			t.Fatalf("port:\n%s", got)
		}
	})
	t.Run("GPU access and mmap are not blocked", func(t *testing.T) {
		got := mustRender(t, UnitInputs{Router: routerBlock(1, 0)})
		for _, bad := range []string{"PrivateDevices", "MemoryDenyWriteExecute", "KillMode"} {
			if strings.Contains(got, bad) {
				t.Fatalf("the unit must not set %s", bad)
			}
		}
		for _, must := range []string{
			"User=localai\n", "Group=localai\n", "SupplementaryGroups=video render\n", "LimitMEMLOCK=infinity\n",
			"WantedBy=multi-user.target\n", "ReadWritePaths=/var/lib/localai\n", "ProtectSystem=strict\n",
			"Restart=on-failure\n", "StartLimitBurst=5\n", "LoadCredential=llama-api-key:/etc/localai/api-key\n",
			"Environment=LLAMA_CACHE=/var/lib/localai/llama-cache\n",
		} {
			if !strings.Contains(got, must) {
				t.Fatalf("missing %q", must)
			}
		}
	})
}

// TestUnitCarriesNoKey: the key reaches the process only through LoadCredential. The unit names
// the credential file and the %d path, never a value.
func TestUnitCarriesNoKey(t *testing.T) {
	for _, in := range []UnitInputs{
		{Router: routerBlock(1, 512)},
		{Mode: LlamaModeSingle, Single: singleN3()},
	} {
		got := mustRender(t, in)
		argv := execStartArgv(t, got)
		for i, a := range argv {
			if a == "--api-key" {
				t.Fatalf("a key on the command line: %q", argv)
			}
			if a == "--api-key-file" && argv[i+1] != "%d/llama-api-key" {
				t.Fatalf("the key file is %q", argv[i+1])
			}
		}
		if strings.Contains(strings.ToLower(got), "bearer") || strings.Contains(got, "API_KEY") {
			t.Fatalf("key material in the unit:\n%s", got)
		}
	}
}

func TestSingleFlagTable(t *testing.T) {
	// Every row of the table of llama-router.md section 3.4, then the rule for the rest.
	rows := map[string]string{
		"model": "-m", "c": "-c", "n-gpu-layers": "-ngl", "alias": "--alias",
		"flash-attn": "--flash-attn", "parallel": "--parallel", "spec-type": "--spec-type",
		"spec-draft-n-max": "--spec-draft-n-max", "model-draft": "--model-draft", "spec-draft-ngl": "--spec-draft-ngl",
		"mmproj": "--mmproj", "chat-template": "--chat-template", "chat-template-file": "--chat-template-file",
		"tags": "--tags", "no-mmap": "--no-mmap", "temp": "--temp",
		// one-character keys and the short-only spellings are short flags
		"b": "-b", "t": "-t", "ub": "-ub", "ctk": "-ctk", "ctv": "-ctv", "fa": "-fa",
	}
	for k, want := range rows {
		if got := SingleFlag(k); got != want {
			t.Errorf("SingleFlag(%q) = %q, want %q", k, got, want)
		}
	}
}

func singleModelWith(extra ...string) *SingleModel {
	m := llamaModel("m1", []string{"chat"}, 1000, 1000,
		[]catalog.ModelFile{fileFor("m1", "weights", "w.gguf")},
		&catalog.Llama{Ctx: 4096, NGPULayers: all(), ExtraArgs: extra})
	return &SingleModel{Model: &m, Root: rootInternal}
}

func TestUnitSingleExtraArgsAndBareFlags(t *testing.T) {
	got := mustRender(t, UnitInputs{Mode: LlamaModeSingle, Single: singleModelWith("--no-mmap", "-b 2048", "-ub 512", "--temp 0.7", "--cache-type-k=q8_0")})
	argv := execStartArgv(t, got)
	want := []string{
		"/opt/localai/llama.cpp/current/bin/llama-server",
		"-m", "/srv/localai/models/m1/w.gguf", "--alias", "m1", "--tags", "chat",
		"-c", "4096", "-ngl", "all",
		"--no-mmap", "-b", "2048", "-ub", "512", "--temp", "0.7", "--cache-type-k", "q8_0",
		"--host", "127.0.0.1", "--port", "8080", "--api-key-file", "%d/llama-api-key",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv\n got %q\nwant %q", argv, want)
	}
	// A bare flag is on its own line, with no value after it.
	if !strings.Contains(got, "  --no-mmap \\\n") {
		t.Fatalf("bare flag:\n%s", got)
	}
}

func TestUnitSingleAliases(t *testing.T) {
	sm := singleModelWith()
	sm.Aliases = []string{"old-name", "old-name", "m1", "Bad Alias", "a,b", "x;y", "fine.one"}
	r, err := RenderUnit(UnitInputs{Mode: LlamaModeSingle, Single: sm})
	if err != nil {
		t.Fatal(err)
	}
	argv := execStartArgv(t, string(r.Data))
	var alias string
	for i, a := range argv {
		if a == "--alias" {
			alias = argv[i+1]
		}
	}
	if alias != "m1,old-name,fine.one" {
		t.Fatalf("alias = %q", alias)
	}
	reasons := map[string]string{}
	for _, d := range r.DroppedAliases {
		reasons[d.alias] = d.reason
	}
	for _, a := range []string{"m1", "Bad Alias", "a,b", "x;y"} {
		if reasons[a] == "" {
			t.Errorf("alias %q was not reported (%v)", a, reasons)
		}
	}
	if len(r.DroppedAliases) != 4 {
		t.Fatalf("dropped %+v", r.DroppedAliases)
	}
}

// TestUnitQuoting: a path with white space or characters systemd treats specially survives the
// round trip through the unit file (ark drives are mounted under names the user chose).
func TestUnitQuoting(t *testing.T) {
	roots := []string{
		"/media/user/My Drive", "/media/user/it's", `/media/user/a"b`, `/media/user/back\slash`,
		"/media/user/100%", "/media/user/$HOME", "/media/user/a b  c",
	}
	for _, root := range roots {
		sm := singleModelWith()
		sm.Root = root
		got := mustRender(t, UnitInputs{Mode: LlamaModeSingle, Single: sm})
		argv := execStartArgv(t, got)
		want := root + "/models/m1/w.gguf"
		if argv[2] != want {
			t.Errorf("root %q: argv[2] = %q, want %q\n%s", root, argv[2], want, got)
		}
		if strings.Contains(got, "ExecStart=/opt/localai/llama.cpp/current/bin/llama-server \\\n  -m ") == false {
			t.Errorf("shape:\n%s", got)
		}
	}
	if q := quoteUnitArg("plain-1.2"); q != "plain-1.2" {
		t.Errorf("a plain word was changed: %q", q)
	}
	if q := quoteUnitArg(""); q != `""` {
		t.Errorf("an empty word: %q", q)
	}
}

func TestUnitRefusals(t *testing.T) {
	cases := []struct {
		name    string
		in      UnitInputs
		subject string
	}{
		{"relative binary", UnitInputs{Binary: "bin/llama-server"}, "binary"},
		{"binary with a space", UnitInputs{Binary: "/opt/local ai/llama-server"}, "binary"},
		{"binary with a semicolon", UnitInputs{Binary: "/opt/x;rm/llama-server"}, "binary"},
		{"binary with a percent", UnitInputs{Binary: "/opt/%h/llama-server"}, "binary"},
		{"binary with a dollar", UnitInputs{Binary: "/opt/$X/llama-server"}, "binary"},
		{"binary with a newline", UnitInputs{Binary: "/opt/x\nExecStartPre=/bin/sh/llama-server"}, "binary"},
		{"binary with dot dot", UnitInputs{Binary: "/opt/../etc/llama-server"}, "binary"},
		{"binary ending in a slash", UnitInputs{Binary: "/opt/localai/"}, "binary"},
		{"binary with a double slash", UnitInputs{Binary: "/opt//llama-server"}, "binary"},
		{"port too big", UnitInputs{Port: 70000}, "port"},
		{"negative port", UnitInputs{Port: -1}, "port"},
		{"unknown mode", UnitInputs{Mode: "cluster"}, "mode"},
		{"single without a model", UnitInputs{Mode: LlamaModeSingle}, "single"},
		{"single with an empty model", UnitInputs{Mode: LlamaModeSingle, Single: &SingleModel{}}, "single"},
	}
	for _, c := range cases {
		_, err := RenderUnit(c.in)
		ue, ok := err.(*UnitError)
		if !ok {
			t.Errorf("%s: want a *UnitError, got %v", c.name, err)
			continue
		}
		if ue.Subject != c.subject {
			t.Errorf("%s: subject %q, want %q", c.name, ue.Subject, c.subject)
		}
	}
	t.Run("a model that cannot be a preset is refused with its id", func(t *testing.T) {
		sm := singleModelWith()
		sm.Model.Files = nil
		_, err := RenderUnit(UnitInputs{Mode: LlamaModeSingle, Single: sm})
		if ue, ok := err.(*UnitError); !ok || ue.Subject != "m1" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unsafe path is refused", func(t *testing.T) {
		for _, root := range []string{"/media/x;y", "/media/x#y", "/media/x\ny", " /media/x", "relative"} {
			sm := singleModelWith()
			sm.Root = root
			if _, err := RenderUnit(UnitInputs{Mode: LlamaModeSingle, Single: sm}); err == nil {
				t.Errorf("root %q was accepted", root)
			}
		}
	})
	t.Run("an extra_args entry the router owns is refused", func(t *testing.T) {
		for _, bad := range []string{"--port 9", "--host 0.0.0.0", "--api-key abc", "-m other.gguf"} {
			if _, err := RenderUnit(UnitInputs{Mode: LlamaModeSingle, Single: singleModelWith(bad)}); err == nil {
				t.Errorf("%q was accepted", bad)
			}
		}
	})
}

// TestUnitDeterminism: the same inputs give the same bytes, the unit round-trips through a
// systemd-style reader to exactly the argv the inputs call for, and no input reaches outside
// ExecStart.
func TestUnitDeterminism(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	words := []string{"alpha", "My Drive", "x y", "q'z", `d"q`, `b\s`, "p%q", "$v", "a.b-c_d", "é"}
	pick := func() string { return words[rng.Intn(len(words))] }
	for i := 0; i < 300; i++ {
		var in UnitInputs
		if rng.Intn(2) == 0 {
			in = UnitInputs{Router: routerBlock(rng.Intn(6), rng.Intn(4096)-100), Port: 1 + rng.Intn(65535)}
		} else {
			sm := singleModelWith()
			sm.Root = "/media/" + pick() + "/" + pick()
			sm.Model.Llama.Ctx = 1 + rng.Intn(1<<20)
			sm.Model.Llama.Parallel = rng.Intn(5)
			if rng.Intn(2) == 0 {
				sm.Model.Llama.FlashAttn = []string{"on", "off", "auto"}[rng.Intn(3)]
			}
			sm.Aliases = []string{"al-" + fmt.Sprint(rng.Intn(50))}
			in = UnitInputs{Mode: LlamaModeSingle, Single: sm, Port: 1 + rng.Intn(65535)}
		}
		a, errA := RenderUnit(in)
		b, errB := RenderUnit(in)
		if errA != nil || errB != nil {
			t.Fatalf("input %d: %v / %v", i, errA, errB)
		}
		if string(a.Data) != string(b.Data) {
			t.Fatalf("input %d rendered two ways", i)
		}
		argv := execStartArgv(t, string(a.Data))
		if argv[0] != DefaultLlamaBinary {
			t.Fatalf("argv[0] = %q", argv[0])
		}
		if in.Mode == LlamaModeSingle {
			if want := in.Single.Root + "/models/m1/w.gguf"; argv[2] != want {
				t.Fatalf("input %d: model path %q, want %q", i, argv[2], want)
			}
		}
		// Whatever the input, these never move.
		for _, must := range []string{"User=localai\n", "ReadWritePaths=/var/lib/localai\n", "NoNewPrivileges=yes\n"} {
			if !strings.Contains(string(a.Data), must) {
				t.Fatalf("input %d lost %q", i, must)
			}
		}
		if strings.Count(string(a.Data), "ExecStart=") != 1 {
			t.Fatalf("input %d: ExecStart is not unique", i)
		}
	}
}
