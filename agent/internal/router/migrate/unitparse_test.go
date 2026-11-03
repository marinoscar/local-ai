package migrate

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseTheAiN3UnitVerbatim(t *testing.T) {
	u, err := ParseUnit(fixture(t, "llama-qwen.ai-n3.service"))
	if err != nil {
		t.Fatal(err)
	}
	if u.User != "labuser" || u.Group != "labuser" || u.WorkingDirectory != "/home/labuser/llama.cpp" || u.SupplementaryGroups != "video render" {
		t.Fatalf("identity: %+v", u)
	}
	if !reflect.DeepEqual(u.Credentials, []Credential{{ID: "llama-api-key", Path: "/etc/llama-qwen/api-key"}}) {
		t.Fatalf("credentials: %+v", u.Credentials)
	}
	if len(u.Env) != 0 || len(u.DropIns) != 0 || len(u.Sources) != 1 {
		t.Fatalf("env/dropins/sources: %+v", u)
	}
	f, err := ParseExec(u.ExecStart)
	if err != nil {
		t.Fatal(err)
	}
	want := &ExecFlags{
		Binary: "/home/labuser/llama.cpp/build-cuda/bin/llama-server",
		Model:  "/home/labuser/models/qwen3.5-9b-mtp/Qwen3.5-9B-Q4_K_M.gguf", Aliases: []string{"qwen3.5-9b"},
		Ctx: 32768, NGL: "99", FlashAttn: "on", Parallel: 1, Host: "127.0.0.1", Port: 8080,
		APIKeyFile: "%d/llama-api-key", SpecType: "draft-mtp", SpecNMax: 3, SpecNMaxSet: true,
	}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("flags:\n got %+v\nwant %+v", f, want)
	}
}

func TestParseTheAiN1UnitWithItsDropIn(t *testing.T) {
	u, err := ParseUnit(fixture(t, "llama-qwen.ai-n1.service"))
	if err != nil {
		t.Fatal(err)
	}
	if len(u.DropIns) != 1 || u.DropIns[0] != "/etc/systemd/system/llama-qwen.service.d/vulkan.conf" || len(u.Sources) != 2 {
		t.Fatalf("sources: %+v", u)
	}
	got := map[string]string{}
	for _, e := range u.Env {
		got[e.Name] = e.Value
	}
	want := map[string]string{"GGML_VK_SUBALLOCATION_BLOCK_SIZE": "4294967296", "LLAMA_NOTE": "two words", "CUDA_VISIBLE_DEVICES": "0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %v", got)
	}
	f, err := ParseExec(u.ExecStart)
	if err != nil {
		t.Fatal(err)
	}
	if f.Model != "/home/labuser/models/qwen3-coder-next/Qwen3-Coder-Next-Q4_K_M-00001-of-00002.gguf" ||
		f.Draft != "/home/labuser/models/qwen3-coder-next/mtp-draft-Q4_0.gguf" || f.SpecDraftNGL != "all" || f.Ctx != 131072 || f.FlashAttn != "auto" {
		t.Fatalf("flags: %+v", f)
	}
}

func TestDropInsOverrideLikeSystemd(t *testing.T) {
	text := `# /etc/systemd/system/llama-qwen.service
[Service]
User=a
ExecStart=/x/llama-server -m /m/a.gguf
Environment=GGML_A=1 GGML_B=2
# /etc/systemd/system/llama-qwen.service.d/o.conf
[Service]
User=b
ExecStart=
ExecStart=/y/llama-server -m /m/b.gguf --alias z
Environment=GGML_A=9
`
	u, err := ParseUnit(text)
	if err != nil {
		t.Fatal(err)
	}
	if u.User != "b" || u.ExecStart[0] != "/y/llama-server" || len(u.ExecStart) != 5 {
		t.Fatalf("%+v", u)
	}
	if len(u.Env) != 2 || u.Env[0].Value != "9" || u.Env[1].Value != "2" {
		t.Fatalf("env %+v", u.Env)
	}
	// An empty Environment= resets the list.
	u, err = ParseUnit(text + "Environment=\n")
	if err != nil || len(u.Env) != 0 {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestParseUnitRefusals(t *testing.T) {
	for name, text := range map[string]string{
		"no exec":       "# /etc/systemd/system/llama-qwen.service\n[Service]\nUser=a\n",
		"not found":     "No files found for llama-qwen.service.\n",
		"open quote":    "[Service]\nExecStart=/x/llama-server -m \"/a b\n",
		"dangling":      "[Service]\nExecStart=/x/llama-server \\",
		"bad cred":      "[Service]\nExecStart=/x/llama-server\nLoadCredential=nope\n",
		"unterminated2": "[Service]\nExecStart=/x/llama-server\nEnvironment=\"A=b\n",
	} {
		if _, err := ParseUnit(text); !errors.Is(err, ErrUnitSyntax) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestParseExecForms(t *testing.T) {
	cases := []struct {
		name string
		argv string
		want func(*ExecFlags) bool
	}{
		{"equals forms", "/b/llama-server --ctx-size=4096 --port=9090 --host=0.0.0.0 --alias=a,b", func(f *ExecFlags) bool {
			return f.Ctx == 4096 && f.Port == 9090 && f.Host == "0.0.0.0" && reflect.DeepEqual(f.Aliases, []string{"a", "b"})
		}},
		{"bare -fa", "/b/llama-server -fa -m /x.gguf", func(f *ExecFlags) bool { return f.FlashAttn == "on" && f.Model == "/x.gguf" }},
		{"-fa value", "/b/llama-server -fa off", func(f *ExecFlags) bool { return f.FlashAttn == "off" }},
		{"extras", "/b/llama-server --jinja --temp 0.7 -t 8 --no-webui --seed -1", func(f *ExecFlags) bool {
			return reflect.DeepEqual(f.Extra, []string{"--jinja", "--temp 0.7", "-t 8", "--no-webui", "--seed -1"})
		}},
		{"inline key never kept", "/b/llama-server --api-key s3cret-value --port 8080", func(f *ExecFlags) bool {
			return f.InlineKey && f.Port == 8080 && !strings.Contains(strings.Join(f.Extra, " "), "s3cret")
		}},
		{"defaults", "/b/llama-server", func(f *ExecFlags) bool { return f.Port == 0 && f.Host == "" && f.Model == "" }},
	}
	for _, c := range cases {
		f, err := ParseExec(strings.Fields(c.argv))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !c.want(f) {
			t.Errorf("%s: got %+v", c.name, f)
		}
	}
	for name, argv := range map[string]string{
		"bash wrapper": "/bin/bash -c x",
		"env ref":      "/b/llama-server -m $MODEL",
		"stray word":   "/b/llama-server stray",
		"no value":     "/b/llama-server -c",
		"bad number":   "/b/llama-server -c many",
		"bad port":     "/b/llama-server --port x",
	} {
		if _, err := ParseExec(strings.Fields(argv)); !errors.Is(err, ErrUnitSyntax) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := ParseExec(nil); !errors.Is(err, ErrUnitSyntax) {
		t.Errorf("empty: %v", err)
	}
}

func TestSplitWords(t *testing.T) {
	got, err := splitWords(`a "b c" 'd e' f\ g "h\"i" ""`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b c", "d e", "f g", `h"i`, ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func llamaModel(l catalog.Llama) *catalog.Model { return &catalog.Model{ID: "m", Llama: &l} }

func TestDiffCatalog(t *testing.T) {
	all := catalog.IntOrAll{Set: true, All: true}
	n99 := catalog.IntOrAll{Set: true, N: 99}
	n20 := catalog.IntOrAll{Set: true, N: 20}
	base := ExecFlags{Ctx: 4096, NGL: "99", FlashAttn: "on", Parallel: 1}
	cat := catalog.Llama{Ctx: 4096, NGPULayers: n99, FlashAttn: "on", Parallel: 1}
	cases := []struct {
		name       string
		mutF       func(*ExecFlags)
		mutC       func(*catalog.Llama)
		draft, mm  string
		wantFlags  []string
		wantCounts int
	}{
		{name: "identical"},
		{name: "ctx differs", mutF: func(f *ExecFlags) { f.Ctx = 8192 }, wantFlags: []string{"-c"}},
		{name: "ctx unset in unit", mutF: func(f *ExecFlags) { f.Ctx = 0 }, wantFlags: []string{"-c"}},
		{name: "ngl all equals 99", mutC: func(l *catalog.Llama) { l.NGPULayers = all }},
		{name: "ngl text all equals 99", mutF: func(f *ExecFlags) { f.NGL = "all" }},
		{name: "ngl 20 vs 99", mutF: func(f *ExecFlags) { f.NGL = "20" }, wantFlags: []string{"-ngl"}},
		{name: "ngl partial equal", mutF: func(f *ExecFlags) { f.NGL = "20" }, mutC: func(l *catalog.Llama) { l.NGPULayers = n20 }},
		{name: "ngl unset in unit", mutF: func(f *ExecFlags) { f.NGL = "" }, wantFlags: []string{"-ngl"}},
		{name: "flash attn default is auto", mutF: func(f *ExecFlags) { f.FlashAttn = "" }, mutC: func(l *catalog.Llama) { l.FlashAttn = "auto" }},
		{name: "flash attn differs", mutF: func(f *ExecFlags) { f.FlashAttn = "off" }, wantFlags: []string{"--flash-attn"}},
		{name: "parallel differs", mutF: func(f *ExecFlags) { f.Parallel = 4 }, wantFlags: []string{"--parallel"}},
		{name: "spec type", mutF: func(f *ExecFlags) { f.SpecType = "draft-mtp" }, wantFlags: []string{"--spec-type"}},
		{name: "spec n_max", mutF: func(f *ExecFlags) { f.SpecType = "draft-mtp"; f.SpecNMax = 2 },
			mutC:      func(l *catalog.Llama) { l.Speculative = &catalog.Speculative{Type: "draft-mtp", NMax: 3} },
			wantFlags: []string{"--spec-draft-n-max"}},
		{name: "draft file present and equal", draft: "draft",
			mutF: func(f *ExecFlags) { f.Draft = "/d.gguf"; f.SpecDraftNGL = "all" },
			mutC: func(l *catalog.Llama) { l.Speculative = &catalog.Speculative{DraftFileKind: "draft", DraftNGL: all} }},
		{name: "draft in unit only", draft: "draft", mutF: func(f *ExecFlags) { f.Draft = "/d.gguf" }, wantFlags: []string{"-md"}},
		{name: "draft ngl differs", draft: "draft",
			mutF:      func(f *ExecFlags) { f.Draft = "/d.gguf"; f.SpecDraftNGL = "10" },
			mutC:      func(l *catalog.Llama) { l.Speculative = &catalog.Speculative{DraftFileKind: "draft", DraftNGL: all} },
			wantFlags: []string{"--spec-draft-ngl"}},
		{name: "mmproj in catalog only", mutC: func(l *catalog.Llama) { l.MmprojFileKind = "mmproj" }, wantFlags: []string{"--mmproj"}},
		{name: "extra in unit only", mutF: func(f *ExecFlags) { f.Extra = []string{"--jinja"} }, wantFlags: []string{"--jinja"}},
		{name: "extra equal in other forms", mutF: func(f *ExecFlags) { f.Extra = []string{"--temp 0.7"} },
			mutC: func(l *catalog.Llama) { l.ExtraArgs = []string{"--temp=0.7"} }},
		{name: "extra split form", mutF: func(f *ExecFlags) { f.Extra = []string{"--temp 0.7"} },
			mutC: func(l *catalog.Llama) { l.ExtraArgs = []string{"--temp", "0.7"} }},
		{name: "extra in catalog only", mutC: func(l *catalog.Llama) { l.ExtraArgs = []string{"--no-mmap"} }, wantFlags: []string{"--no-mmap"}},
	}
	for _, c := range cases {
		f, l := base, cat
		if c.mutF != nil {
			c.mutF(&f)
		}
		if c.mutC != nil {
			c.mutC(&l)
		}
		var got []string
		for _, d := range DiffCatalog(&f, llamaModel(l), c.draft, c.mm) {
			got = append(got, d.Flag)
		}
		if !reflect.DeepEqual(got, c.wantFlags) {
			t.Errorf("%s: diff flags = %v, want %v", c.name, got, c.wantFlags)
		}
	}
	// A model with no llama block compares against an empty one.
	if d := DiffCatalog(&ExecFlags{}, &catalog.Model{ID: "x"}, "", ""); len(d) != 0 {
		t.Errorf("empty = %v", d)
	}
	if s := (FlagDiff{Flag: "-c", Unit: "1", Catalog: "2"}).String(); !strings.Contains(s, "-c") {
		t.Errorf("String = %q", s)
	}
}
