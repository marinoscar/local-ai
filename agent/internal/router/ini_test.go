package router

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// pairsText renders pairs as "key=value" lines, the form the tables compare.
func pairsText(p Preset) []string {
	out := make([]string, len(p.Pairs))
	for i, pr := range p.Pairs {
		out[i] = pr.Key + "=" + pr.Value
	}
	return out
}

func mustPreset(t *testing.T, m catalog.Model, aliases ...string) Preset {
	t.Helper()
	p, err := BuildPreset(&m, rootInternal, aliases)
	if err != nil {
		t.Fatalf("BuildPreset: %v", err)
	}
	return p
}

// base is a weights-only model with the minimum llama block.
func base() catalog.Model {
	return llamaModel("model-a-q4km", []string{"chat"}, 100, 100,
		[]catalog.ModelFile{fileFor("model-a-q4km", "weights", "A-Q4_K_M.gguf")},
		&catalog.Llama{Ctx: 4096})
}

const dirA = "/srv/localai/models/model-a-q4km/"

// TestMappingTable is one case per row of llama-router.md section 3.5.1.
func TestMappingTable(t *testing.T) {
	withFiles := func(m *catalog.Model, files ...catalog.ModelFile) {
		m.Files = append(m.Files, files...)
	}
	cases := []struct {
		name string
		mut  func(*catalog.Model)
		al   []string
		want []string
	}{
		{"id and weights path, ctx always pinned", func(m *catalog.Model) {}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "tags=chat"}},
		{"n_gpu_layers integer", func(m *catalog.Model) { m.Llama.NGPULayers = num(99) }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "n-gpu-layers=99", "tags=chat"}},
		{"n_gpu_layers all", func(m *catalog.Model) { m.Llama.NGPULayers = all() }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "n-gpu-layers=all", "tags=chat"}},
		{"n_gpu_layers zero is explicit", func(m *catalog.Model) { m.Llama.NGPULayers = num(0) }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "n-gpu-layers=0", "tags=chat"}},
		{"flash_attn on", func(m *catalog.Model) { m.Llama.FlashAttn = "on" }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "flash-attn=on", "tags=chat"}},
		{"flash_attn off", func(m *catalog.Model) { m.Llama.FlashAttn = "off" }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "flash-attn=off", "tags=chat"}},
		{"flash_attn auto", func(m *catalog.Model) { m.Llama.FlashAttn = "auto" }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "flash-attn=auto", "tags=chat"}},
		{"parallel", func(m *catalog.Model) { m.Llama.Parallel = 2 }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "parallel=2", "tags=chat"}},
		{"speculative type and n_max", func(m *catalog.Model) {
			m.Llama.Speculative = &catalog.Speculative{Type: "draft-mtp", NMax: 3}
		}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "spec-type=draft-mtp", "spec-draft-n-max=3", "tags=chat"}},
		{"speculative draft file and ngl all", func(m *catalog.Model) {
			withFiles(m, fileFor(m.ID, "draft", "draft-Q4_0.gguf"))
			m.Llama.Speculative = &catalog.Speculative{Type: "draft", NMax: 8, DraftFileKind: "draft", DraftNGL: all()}
		}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "spec-type=draft", "spec-draft-n-max=8",
				"model-draft=" + dirA + "draft-Q4_0.gguf", "spec-draft-ngl=all", "tags=chat"}},
		{"speculative draft ngl integer", func(m *catalog.Model) {
			withFiles(m, fileFor(m.ID, "draft", "draft-Q4_0.gguf"))
			m.Llama.Speculative = &catalog.Speculative{Type: "draft", DraftFileKind: "draft", DraftNGL: num(20)}
		}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "spec-type=draft",
				"model-draft=" + dirA + "draft-Q4_0.gguf", "spec-draft-ngl=20", "tags=chat"}},
		{"draft_ngl alone is not rendered without model-draft", func(m *catalog.Model) {
			m.Llama.Speculative = &catalog.Speculative{Type: "draft-mtp", DraftNGL: all()}
		}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "spec-type=draft-mtp", "tags=chat"}},
		{"mmproj", func(m *catalog.Model) {
			withFiles(m, fileFor(m.ID, "mmproj", "mmproj-F16.gguf"))
			m.Llama.MmprojFileKind = "mmproj"
		}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "mmproj=" + dirA + "mmproj-F16.gguf", "tags=chat"}},
		{"chat template builtin name", func(m *catalog.Model) { m.Llama.ChatTemplate = "chatml" }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "chat-template=chatml", "tags=chat"}},
		{"chat template file kind", func(m *catalog.Model) {
			withFiles(m, fileFor(m.ID, "chat_template", "template.jinja"))
			m.Llama.ChatTemplate = "chat_template"
		}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "chat-template-file=" + dirA + "template.jinja", "tags=chat"}},
		{"extra_args --foo bar", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"--ctx-checkpoints 4"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "ctx-checkpoints=4", "tags=chat"}},
		{"extra_args --foo (flag)", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"--jinja"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "jinja=true", "tags=chat"}},
		{"extra_args --no-foo", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"--no-mmap"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "no-mmap=true", "tags=chat"}},
		{"extra_args -x v", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"-ub 512"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "ub=512", "tags=chat"}},
		{"extra_args --foo=bar", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"--cache-type-k=q8_0"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "cache-type-k=q8_0", "tags=chat"}},
		{"extra_args split into two entries", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"--temp", "0.7", "--jinja"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "temp=0.7", "jinja=true", "tags=chat"}},
		{"extra_args negative number as a split value", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"--seed", "-1"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "seed=-1", "tags=chat"}},
		{"roles joined in catalog order", func(m *catalog.Model) { m.Roles = []string{"reasoning", "chat", "coder"} }, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "tags=reasoning,chat,coder"}},
		{"legacy aliases comma-joined", func(m *catalog.Model) {}, []string{"old-name", "older-name"},
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "tags=chat", "alias=old-name,older-name"}},
		{"unmapped fields are not rendered", func(m *catalog.Model) {
			m.Summary = "a summary"
			m.ClientHints = &catalog.ClientHints{MaxOutputTokens: 4096, Reasoning: true}
			m.Llama.Env = map[string]string{"GGML_VK_SUBALLOCATION_BLOCK_SIZE": "4294967296"}
		}, nil,
			[]string{"model=" + dirA + "A-Q4_K_M.gguf", "c=4096", "tags=chat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mut(&m)
			equalStrings(t, pairsText(mustPreset(t, m, tc.al...)), tc.want)
		})
	}
}

// TestMappingTableAllKeysInOrder checks the table order when every optional row is present, and
// that load-on-startup comes last, only in the default's section.
func TestMappingTableAllKeysInOrder(t *testing.T) {
	m := base()
	m.Roles = []string{"chat", "vision"}
	m.Files = append(m.Files, fileFor(m.ID, "draft", "d.gguf"), fileFor(m.ID, "mmproj", "m.gguf"), fileFor(m.ID, "chat_template", "t.jinja"))
	m.Llama = &catalog.Llama{
		Ctx: 8192, NGPULayers: all(), FlashAttn: "auto", Parallel: 2,
		Speculative:    &catalog.Speculative{Type: "draft", NMax: 4, DraftFileKind: "draft", DraftNGL: num(7)},
		MmprojFileKind: "mmproj", ChatTemplate: "chat_template", ExtraArgs: []string{"--jinja", "--ctx-checkpoints 2"},
	}
	p := mustPreset(t, m, "alias-one")
	var keys []string
	for _, pr := range p.Pairs {
		keys = append(keys, pr.Key)
	}
	equalStrings(t, keys, []string{"model", "c", "n-gpu-layers", "flash-attn", "parallel", "spec-type", "spec-draft-n-max",
		"model-draft", "spec-draft-ngl", "mmproj", "chat-template-file", "jinja", "ctx-checkpoints", "tags", "alias"})

	out, err := Render(Header{At: fixedTime()}, []Preset{p}, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if lines[len(lines)-1] != "load-on-startup = true" {
		t.Fatalf("load-on-startup is not the last key of the default's section: %q", lines[len(lines)-1])
	}
	out, _ = Render(Header{At: fixedTime()}, []Preset{p}, "someone-else")
	if strings.Count(string(out), "load-on-startup = true") != 0 {
		t.Fatal("a section that is not the default got load-on-startup = true")
	}
}

func TestMultiShardPicksFirstShard(t *testing.T) {
	m := base()
	m.Files = []catalog.ModelFile{
		fileFor(m.ID, "weights", "A-Q4_K_M-00003-of-00003.gguf"),
		fileFor(m.ID, "weights", "A-Q4_K_M-00001-of-00003.gguf"),
		fileFor(m.ID, "weights", "A-Q4_K_M-00002-of-00003.gguf"),
	}
	got := pairsText(mustPreset(t, m))[0]
	if got != "model="+dirA+"A-Q4_K_M-00001-of-00003.gguf" {
		t.Fatalf("model = %q", got)
	}
}

// TestPresetRejections: every way an entry can be refused, each a *PresetError (LAI-LLM-007).
func TestPresetRejections(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*catalog.Model)
		sub  string
	}{
		{"path with ';'", func(m *catalog.Model) { m.Files[0].Name = "a;b.gguf" }, "';' or '#'"},
		{"path with '#'", func(m *catalog.Model) { m.Files[0].Name = "a#b.gguf" }, "';' or '#'"},
		{"path with a newline", func(m *catalog.Model) { m.Files[0].Name = "a\nb.gguf" }, "control character"},
		{"name with a trailing space", func(m *catalog.Model) { m.Files[0].Name = "a.gguf " }, "leading or trailing"},
		{"name that climbs out", func(m *catalog.Model) { m.Files[0].Name = "../x/a.gguf" }, "plain relative"},
		{"absolute name", func(m *catalog.Model) { m.Files[0].Name = "/etc/passwd" }, "not relative"},
		{"unclean name", func(m *catalog.Model) { m.Files[0].Name = "./a.gguf" }, "plain relative"},
		{"id with ']'", func(m *catalog.Model) { m.ID = "bad]id" }, "section name"},
		{"id with a newline", func(m *catalog.Model) { m.ID = "bad\nid" }, "section name"},
		{"no llama block", func(m *catalog.Model) { m.Llama = nil }, "no llama block"},
		{"zero ctx", func(m *catalog.Model) { m.Llama.Ctx = 0 }, "llama.ctx"},
		{"no weights file", func(m *catalog.Model) { m.Files[0].Kind = "mmproj" }, "no files entry of kind weights"},
		{"two weights files and no first shard", func(m *catalog.Model) {
			m.Files = append(m.Files, fileFor(m.ID, "weights", "B.gguf"))
		}, "no single first shard"},
		{"mmproj kind without a file", func(m *catalog.Model) { m.Llama.MmprojFileKind = "mmproj" }, "no files entry of kind mmproj"},
		{"draft kind without a file", func(m *catalog.Model) {
			m.Llama.Speculative = &catalog.Speculative{Type: "draft", DraftFileKind: "draft"}
		}, "no files entry of kind draft"},
		{"chat_template kind without a file", func(m *catalog.Model) { m.Llama.ChatTemplate = "chat_template" }, "kind chat_template"},
		{"bad flash_attn", func(m *catalog.Model) { m.Llama.FlashAttn = "maybe" }, "flash_attn"},
		{"bad speculative type", func(m *catalog.Model) { m.Llama.Speculative = &catalog.Speculative{Type: "magic"} }, "speculative.type"},
		{"chat template with ';'", func(m *catalog.Model) { m.Llama.ChatTemplate = "a;b" }, "template name"},
		{"role with a comma", func(m *catalog.Model) { m.Roles = []string{"a,b"} }, "cannot be a tag"},
		{"extra_args value with ';'", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"--foo a;b"} }, "contains"},
		{"extra_args duplicates a field", func(m *catalog.Model) { m.Llama.ExtraArgs = []string{"-c 8192"} }, "already sets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mut(&m)
			_, err := BuildPreset(&m, rootInternal, nil)
			var pe *PresetError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v, want a *PresetError", err)
			}
			if !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.sub)
			}
		})
	}
	if _, err := BuildPreset(nil, rootInternal, nil); err == nil {
		t.Fatal("a nil entry was accepted")
	}
}

// TestExtraArgsRefusals: the arguments the router owns are refused even though lint already does
// (defence in depth), in every spelling.
func TestExtraArgsRefusals(t *testing.T) {
	for _, a := range []string{
		"--host 0.0.0.0", "--port 9", "--api-key abc", "--api-key-file /x", "-m /x.gguf", "--model /x.gguf",
		"--model-draft /x", "--models-dir /x", "--models-max 4", "--models-preset /x", "--alias x", "-hf org/repo",
		"--hf-repo org/repo", "--mmproj /x", "--host=0.0.0.0", "--PORT 1", "--load-on-startup", "--stop-timeout 1", "--tags x",
	} {
		if _, err := ParseExtraArgs([]string{a}); err == nil {
			t.Errorf("%q was accepted", a)
		}
	}
	for _, a := range []string{"bar", "--", "---x", "--a b c;d", "--foo", "--foo"} {
		args := []string{a}
		if a == "--foo" {
			args = []string{"--foo", "--foo"}
		}
		if _, err := ParseExtraArgs(args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if got, err := ParseExtraArgs([]string{"", "  ", "--jinja"}); err != nil || len(got) != 1 {
		t.Fatalf("blank entries: %v %v", got, err)
	}
}

func TestRenderLayout(t *testing.T) {
	h := Header{AgentVersion: "0.1.0", At: fixedTime(), Node: "ai-n3", Profile: "small-8gb", Catalog: "2026.11.02-1", InputsSHA256: "abc"}
	a := Preset{ID: "b-model", Pairs: []Pair{{"c", "1"}}}
	b := Preset{ID: "a-model", Pairs: []Pair{{"c", "2"}}}
	out, err := Render(h, []Preset{a, b}, "b-model")
	if err != nil {
		t.Fatal(err)
	}
	want := "; GENERATED by aiagent 0.1.0 at " + fixedNow + " — DO NOT EDIT (changes are overwritten).\n" +
		"; node=ai-n3 profile=small-8gb catalog=2026.11.02-1 inputs_sha256=abc\n" +
		"; Spec: docs/specs/llama-router.md §3.5\n" +
		"version = 1\n\n[*]\nstop-timeout = 10\nload-on-startup = false\n" +
		"\n[a-model]\nc = 2\n\n[b-model]\nc = 1\nload-on-startup = true\n"
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	if _, err := Render(h, []Preset{a, a}, ""); err == nil {
		t.Fatal("duplicate presets were rendered")
	}
	empty, _ := Render(h, nil, "")
	if !strings.HasSuffix(string(empty), "load-on-startup = false\n") || strings.Contains(string(empty), "[a") {
		t.Fatalf("empty file: %q", empty)
	}
	// Header values cannot break out of the comment line.
	h.Node = "n\nversion = 2"
	out, _ = Render(h, nil, "")
	if strings.Contains(string(out), "\nversion = 2") {
		t.Fatalf("a newline in a header value reached the file:\n%s", out)
	}
}

func TestStripHeaderLineAndSameBody(t *testing.T) {
	h := Header{AgentVersion: "0.1.0", At: fixedTime(), Node: "n", InputsSHA256: "x"}
	a, _ := Render(h, nil, "")
	h.At = h.At.Add(time.Hour)
	h.AgentVersion = "0.2.0"
	b, _ := Render(h, nil, "")
	if string(a) == string(b) || !SameBody(a, b) {
		t.Fatal("two renderings that differ only in time and version must be SameBody and not equal")
	}
	h.InputsSHA256 = "y"
	c, _ := Render(h, nil, "")
	if SameBody(a, c) {
		t.Fatal("a different inputs hash is a different body")
	}
	if string(StripHeaderLine([]byte("version = 1\n"))) != "version = 1\n" {
		t.Fatal("a file without the header line must come back unchanged")
	}
	if StripHeaderLine([]byte(headerPrefix+"only a line")) != nil {
		t.Fatal("a header with no newline strips to nothing")
	}
}

// TestInputsHash: stable for identical inputs, independent of input order, sensitive to every
// rendered input and blind to the ones that do not reach the file.
func TestInputsHash(t *testing.T) {
	models := aiN3Models()
	mk := func(mut func(*HashInput)) string {
		in := HashInput{
			Node: "ai-n3", Profile: "small-8gb", CatalogVersion: "2026.11.02-1", Default: "qwen3.5-9b-q4km",
			Entries:   []Entry{{Model: &models[0], Root: rootInternal}, {Model: &models[1], Root: rootInternal}},
			Aliases:   map[string][]string{"qwen3.5-9b-q4km": {"b", "a"}},
			AddModels: []string{"x", "y"},
		}
		if mut != nil {
			mut(&in)
		}
		return InputsHash(in)
	}
	ref := mk(nil)
	if len(ref) != 64 || ref != mk(nil) {
		t.Fatalf("hash %q is not 64 hex or not stable", ref)
	}
	same := map[string]func(*HashInput){
		"entry order":  func(in *HashInput) { in.Entries[0], in.Entries[1] = in.Entries[1], in.Entries[0] },
		"alias order":  func(in *HashInput) { in.Aliases = map[string][]string{"qwen3.5-9b-q4km": {"a", "b"}} },
		"add order":    func(in *HashInput) { in.AddModels = []string{"y", "x"} },
		"summary text": func(in *HashInput) { in.Entries[0].Model.Summary = "a new summary" },
		"llama.env": func(in *HashInput) {
			cp := *in.Entries[0].Model
			l := *cp.Llama
			l.Env = map[string]string{"GGML_X": "1"}
			cp.Llama = &l
			in.Entries[0].Model = &cp
		},
	}
	for name, mut := range same {
		if got := mk(mut); got != ref {
			t.Errorf("%s changed the hash", name)
		}
	}
	different := map[string]func(*HashInput){
		"node":    func(in *HashInput) { in.Node = "ai-n2" },
		"profile": func(in *HashInput) { in.Profile = "other" },
		"catalog": func(in *HashInput) { in.CatalogVersion = "2026.11.03-1" },
		"default": func(in *HashInput) { in.Default = "small-vlm-q4km" },
		"root":    func(in *HashInput) { in.Entries[0].Root = "/media/a" },
		"alias":   func(in *HashInput) { in.Aliases = map[string][]string{"qwen3.5-9b-q4km": {"a"}} },
		"removes": func(in *HashInput) { in.RemoveModels = []string{"z"} },
		"a model": func(in *HashInput) { in.Entries = in.Entries[:1] },
		"ctx": func(in *HashInput) {
			cp := *in.Entries[0].Model
			l := *cp.Llama
			l.Ctx++
			cp.Llama = &l
			in.Entries[0].Model = &cp
		},
		"a file name": func(in *HashInput) {
			cp := *in.Entries[0].Model
			cp.Files = []catalog.ModelFile{fileFor(cp.ID, "weights", "other.gguf")}
			in.Entries[0].Model = &cp
		},
	}
	for name, mut := range different {
		if got := mk(mut); got == ref {
			t.Errorf("%s did not change the hash", name)
		}
	}
}

// TestRenderDeterminismProperty: for seeded random catalogs, shuffling every input order and
// repeating the rendering never changes a byte.
func TestRenderDeterminismProperty(t *testing.T) {
	for seed := int64(1); seed <= 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		n := 1 + rng.Intn(6)
		var presets []Preset
		for i := 0; i < n; i++ {
			m := base()
			m.ID = "model-" + string(rune('a'+rng.Intn(26))) + string(rune('a'+i)) + "-q4km"
			m.Files[0] = fileFor(m.ID, "weights", "W-"+m.ID+".gguf")
			m.Llama.Ctx = 1024 * (1 + rng.Intn(64))
			if rng.Intn(2) == 0 {
				m.Llama.NGPULayers = all()
			}
			if rng.Intn(2) == 0 {
				m.Llama.ExtraArgs = []string{"--jinja", "--ctx-checkpoints 3"}
			}
			presets = append(presets, mustPreset(t, m))
		}
		h := Header{AgentVersion: "v", At: fixedTime(), Node: "n", InputsSHA256: "h"}
		def := presets[rng.Intn(n)].ID
		first, err := Render(h, presets, def)
		if err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 5; round++ {
			shuffled := append([]Preset(nil), presets...)
			rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			again, err := Render(h, shuffled, def)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(first) {
				t.Fatalf("seed %d round %d: the rendering depends on input order", seed, round)
			}
		}
		// Sections come out sorted by id, whatever order they went in.
		var ids []string
		for _, line := range strings.Split(string(first), "\n") {
			if strings.HasPrefix(line, "[") && line != "[*]" {
				ids = append(ids, strings.Trim(line, "[]"))
			}
		}
		for i := 1; i < len(ids); i++ {
			if ids[i-1] >= ids[i] {
				t.Fatalf("seed %d: sections not sorted: %v", seed, ids)
			}
		}
	}
}

func TestCheckPath(t *testing.T) {
	good := []string{"/srv/localai/models/a/b.gguf", "/media/user/ARK DRIVE/models/a/b.gguf", "/srv/a-b_c.d/é.gguf"}
	for _, p := range good {
		if err := CheckPath(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	bad := []string{"", "relative/a.gguf", "/a;b", "/a#b", "/a\nb", "/a\rb", "/a\x00b", " /a", "/a ", "/a\tb\x7f"}
	for _, p := range bad {
		if err := CheckPath(p); err == nil {
			t.Errorf("%q was accepted", p)
		}
	}
}
