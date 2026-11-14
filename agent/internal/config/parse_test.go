package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

const minimal = "schema_version: 1\nnode:\n  id: ai-n7\nprofile: cpu-only\n"

func TestExampleParsesWithNoProblems(t *testing.T) {
	cfg, probs := config.Load([]byte(example(t)), nil)
	if len(probs) != 0 {
		t.Fatalf("the agent.md 3.4 example must load clean, got %+v", probs)
	}
	if cfg.SchemaVersion != 1 || cfg.Node.ID != "ai-n3" || cfg.Node.DisplayName != "HP ZBook (ai-n3)" {
		t.Errorf("node block wrong: %+v", cfg.Node)
	}
	if cfg.Node.PublicHost == nil || *cfg.Node.PublicHost != "ai-n3.marin.cr" {
		t.Errorf("public_host wrong: %v", cfg.Node.PublicHost)
	}
	if cfg.Profile != "small-8gb" || cfg.ProfileOverrides.BudgetBytes != nil {
		t.Errorf("profile wrong: %q %v", cfg.Profile, cfg.ProfileOverrides.BudgetBytes)
	}
	l := cfg.Llama
	if l.Mode != "router" || l.Backend != "cuda" || l.Commit != "f3586e66b" || l.CudaArch != nil || l.LoadTimeoutSeconds != 300 {
		t.Errorf("llama block wrong: %+v", l)
	}
	if !reflect.DeepEqual(l.LegacyAliases, map[string][]string{"qwen3.5-9b-q4km": {"qwen3.5-9b"}}) || len(l.ExtraEnv) != 0 {
		t.Errorf("llama maps wrong: %+v %+v", l.LegacyAliases, l.ExtraEnv)
	}
	if !cfg.Tailscale.Manage || cfg.Tailscale.ServePattern != "B" {
		t.Errorf("tailscale wrong: %+v", cfg.Tailscale)
	}
	c := cfg.Cloud
	if c.URL != "https://ai.marin.cr" || c.DevInsecure || c.Concurrency != 1 || len(c.Allow) != 15 ||
		c.Poll != (config.PollBlock{HeartbeatSeconds: 15, ClaimSeconds: 3, StateSeconds: 60}) {
		t.Errorf("cloud wrong: %+v", c)
	}
	if !reflect.DeepEqual(c.Allow, config.CloudJobNames()) {
		t.Errorf("cloud.allow differs from the 15 known names: %v", c.Allow)
	}
	if cfg.Ports != (config.PortsBlock{Agent: 7070, Llama: 8080, Kiwix: 8888, Whisper: 8081, Music: 8082}) {
		t.Errorf("ports wrong: %+v", cfg.Ports)
	}
	if cfg.UI.Language != "auto" || cfg.Logging.Level != "info" || cfg.Update.Repo != "marinoscar/local-ai" || cfg.Update.Channel != "stable" {
		t.Errorf("update/ui/logging wrong: %+v %+v %+v", cfg.Update, cfg.UI, cfg.Logging)
	}
	for _, name := range config.OwnedBlockNames {
		if _, ok := cfg.Blocks[name]; !ok {
			t.Errorf("owned block %q was not kept", name)
		}
	}
	if cfg.SafeDefaults {
		t.Error("a parsed file is not the safe defaults")
	}
}

func TestOwnedBlocksAreKeptAndDecodable(t *testing.T) {
	cfg, probs := config.Load([]byte(example(t)), nil)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	var portal struct {
		Language        string `yaml:"language"`
		EmergencyNumber string `yaml:"emergency_number"`
		Kiosk           bool   `yaml:"kiosk"`
	}
	if err := cfg.Blocks["portal"].DecodeStrict(&portal); err != nil {
		t.Fatal(err)
	}
	if portal.Language != "auto" || portal.EmergencyNumber != "911" || portal.Kiosk {
		t.Errorf("portal block: %+v", portal)
	}
	var sync struct {
		Concurrency int `yaml:"concurrency"`
	}
	if err := cfg.Blocks["sync"].Decode(&sync); err != nil || sync.Concurrency != 2 {
		t.Errorf("sync block: %+v %v", sync, err)
	}
}

func TestMinimalFileTakesDefaults(t *testing.T) {
	cfg, probs := config.Load([]byte(minimal), nil)
	if len(probs) != 0 {
		t.Fatalf("minimal file must load clean: %+v", probs)
	}
	want := config.Defaults()
	want.SchemaVersion = 1
	want.Node.ID = "ai-n7"
	want.Node.DisplayName = "ai-n7" // display_name defaults to the id
	want.Profile = "cpu-only"
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("defaults differ:\n got  %+v\n want %+v", cfg, want)
	}
	// The table of agent.md 3.4, spelled out.
	checks := []struct {
		name      string
		got, want any
	}{
		{"llama.mode", cfg.Llama.Mode, "router"},
		{"llama.backend (undetected)", cfg.Llama.Backend, ""},
		{"llama.commit (not from BUILD.json)", cfg.Llama.Commit, ""},
		{"llama.cuda_arch", cfg.Llama.CudaArch, (*string)(nil)},
		{"llama.load_timeout_seconds", cfg.Llama.LoadTimeoutSeconds, 300},
		{"llama.legacy_aliases", len(cfg.Llama.LegacyAliases), 0},
		{"llama.extra_env", len(cfg.Llama.ExtraEnv), 0},
		{"tailscale.manage", cfg.Tailscale.Manage, false},
		{"tailscale.serve_pattern", cfg.Tailscale.ServePattern, "none"},
		{"cloud.url", cfg.Cloud.URL, "https://ai.marin.cr"},
		{"cloud.allow", len(cfg.Cloud.Allow), 15},
		{"cloud.concurrency", cfg.Cloud.Concurrency, 1},
		{"cloud.poll.heartbeat_seconds", cfg.Cloud.Poll.HeartbeatSeconds, 15},
		{"cloud.poll.claim_seconds", cfg.Cloud.Poll.ClaimSeconds, 3},
		{"cloud.poll.state_seconds", cfg.Cloud.Poll.StateSeconds, 60},
		{"ui.language", cfg.UI.Language, "auto"},
		{"ports.agent", cfg.Ports.Agent, 7070},
		{"ports.llama", cfg.Ports.Llama, 8080},
		{"ports.kiwix", cfg.Ports.Kiwix, 8888},
		{"ports.whisper", cfg.Ports.Whisper, 8081},
		{"logging.level", cfg.Logging.Level, "info"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("default of %s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestDetectionHooksFillBackendAndCommitOnlyWhenAbsent(t *testing.T) {
	h := &config.Hooks{
		DetectBackend: func() string { return "vulkan" },
		BuildCommit:   func() string { return "abcdef1234" },
	}
	cfg, probs := config.Load([]byte(minimal), h)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	if cfg.Llama.Backend != "vulkan" || cfg.Llama.Commit != "abcdef1234" {
		t.Errorf("hooks not applied: %+v", cfg.Llama)
	}
	set := minimal + "llama:\n  backend: cuda\n  commit: 0123456\n"
	cfg, probs = config.Load([]byte(set), h)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	if cfg.Llama.Backend != "cuda" || cfg.Llama.Commit != "0123456" {
		t.Errorf("explicit values must win: %+v", cfg.Llama)
	}
}

func TestSafeDefaults(t *testing.T) {
	c := config.SafeDefaults()
	if !c.SafeDefaults || c.CloudEnabled() || c.Tailscale.Manage || c.Node.ID != "unconfigured" ||
		c.Ports != (config.PortsBlock{Agent: 7070, Llama: 8080, Kiwix: 8888, Whisper: 8081, Music: 8082}) {
		t.Errorf("safe defaults wrong: %+v", c)
	}
	if len(c.Cloud.Allow) != 0 {
		t.Errorf("safe defaults must allow no cloud job: %v", c.Cloud.Allow)
	}
}

func TestUnknownTopLevelKeyIsAWarningAndTheConfigLoads(t *testing.T) {
	_, probs := config.Load([]byte(minimal+"future_feature:\n  enabled: true\n"), nil)
	if probs.HasErrors() {
		t.Fatalf("an unknown top-level key must not fail the load: %+v", probs)
	}
	p, ok := findProblem(probs, "future_feature")
	if !ok || p.Severity != config.SeverityWarning || p.Line != 5 {
		t.Fatalf("want a warning on future_feature at line 5, got %+v", probs)
	}
}

func TestUnknownNestedKeyIsAnError(t *testing.T) {
	_, probs := config.Load([]byte(minimal+"llama:\n  moed: router\n"), nil)
	p, ok := findProblem(probs, "llama.moed")
	if !ok || p.Severity != config.SeverityError {
		t.Fatalf("a typo inside a block must be an error, got %+v", probs)
	}
}

func TestParseProblems(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantPath string
		wantSub  string
	}{
		{"yaml syntax error", "schema_version: 1\nnode: [", "", "not valid YAML"},
		{"top level is a list", "- a\n- b\n", "", "top level must be a map"},
		{"wrong type: text for a number", minimal + "llama:\n  load_timeout_seconds: soon\n", "llama.load_timeout_seconds", "a whole number"},
		{"wrong type: list for text", minimal + "ui:\n  language: [en]\n", "ui.language", "text"},
		{"wrong type: text for a bool", minimal + "cloud:\n  dev_insecure: maybe\n", "cloud.dev_insecure", "true or false"},
		{"wrong type: a scalar for a block", minimal + "llama: fast\n", "llama", "group of settings"},
		{"wrong type: scalar owned block", minimal + "portal: 1\n", "portal", "group of settings"},
		{"repeated key", minimal + "ui:\n  language: en\n  language: es\n", "ui.language", "twice"},
		{"extra_env value is a list", minimal + "llama:\n  extra_env:\n    GGML_A: [1]\n", "llama.extra_env", "map"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, probs := config.Load([]byte(tt.in), nil)
			p, ok := findProblem(probs, tt.wantPath)
			if !ok {
				t.Fatalf("no problem at %q in %+v", tt.wantPath, probs)
			}
			if p.Severity != config.SeverityError || !strings.Contains(p.Message, tt.wantSub) {
				t.Errorf("got %+v, want error containing %q", p, tt.wantSub)
			}
		})
	}
}

func TestExplicitNullMeansDefaultOrNil(t *testing.T) {
	in := minimal + "llama:\n  load_timeout_seconds: null\n  cuda_arch: null\nui:\ncloud:\n  allow:\n"
	cfg, probs := config.Load([]byte(in), nil)
	if len(probs) != 0 {
		t.Fatalf("nulls must load clean: %+v", probs)
	}
	if cfg.Llama.LoadTimeoutSeconds != 300 || cfg.Llama.CudaArch != nil || cfg.UI.Language != "auto" || len(cfg.Cloud.Allow) != 15 {
		t.Errorf("nulls must keep defaults: %+v", cfg)
	}
}

func TestEmptyAllowListMeansNoCloudJobs(t *testing.T) {
	cfg, probs := config.Load([]byte(minimal+"cloud:\n  allow: []\n"), nil)
	if len(probs) != 0 || cfg.Cloud.Allow == nil || len(cfg.Cloud.Allow) != 0 {
		t.Fatalf("allow: [] is valid and empty: %v %+v", cfg.Cloud.Allow, probs)
	}
}

func TestUnquotedNumbersDecodeIntoTextFields(t *testing.T) {
	in := minimal + "llama:\n  cuda_arch: 75\n  extra_env:\n    GGML_VK_SUBALLOCATION_BLOCK_SIZE: 4294967296\n"
	cfg, probs := config.Load([]byte(in), nil)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	if cfg.Llama.CudaArch == nil || *cfg.Llama.CudaArch != "75" || cfg.Llama.ExtraEnv["GGML_VK_SUBALLOCATION_BLOCK_SIZE"] != "4294967296" {
		t.Errorf("got %+v", cfg.Llama)
	}
}

func TestCloudURLTrailingSlashIsNormalised(t *testing.T) {
	cfg, probs := config.Load([]byte(minimal+"cloud:\n  url: https://cloud.example.test/\n"), nil)
	if len(probs) != 0 || cfg.Cloud.URL != "https://cloud.example.test" {
		t.Fatalf("got %q %+v", cfg.Cloud.URL, probs)
	}
}

func TestEmptyFileReportsTheRequiredFields(t *testing.T) {
	_, probs := config.Load(nil, nil)
	for _, path := range []string{"schema_version", "node.id", "profile"} {
		if _, ok := findProblem(probs, path); !ok {
			t.Errorf("an empty file must report %s as required, got %+v", path, probs)
		}
	}
}

func TestAliasesAreFollowed(t *testing.T) {
	in := "schema_version: 1\nnode: &n\n  id: ai-n2\nprofile: cpu-only\nui: &u\n  language: es\n"
	cfg, probs := config.Load([]byte(in), nil)
	if len(probs) != 0 || cfg.Node.ID != "ai-n2" || cfg.UI.Language != "es" {
		t.Fatalf("got %+v %+v", cfg, probs)
	}
}

func TestCloneIsDeep(t *testing.T) {
	cfg, probs := config.Load([]byte(example(t)), nil)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	cp := cfg.Clone()
	cp.Cloud.Allow[0] = "changed"
	cp.Llama.LegacyAliases["qwen3.5-9b-q4km"][0] = "changed"
	cp.Llama.ExtraEnv["GGML_X"] = "1"
	*cp.Node.PublicHost = "changed"
	cp.Blocks["portal"][0] = 'X'
	if cfg.Cloud.Allow[0] == "changed" || cfg.Llama.LegacyAliases["qwen3.5-9b-q4km"][0] == "changed" ||
		len(cfg.Llama.ExtraEnv) != 0 || *cfg.Node.PublicHost == "changed" || cfg.Blocks["portal"][0] == 'X' {
		t.Fatal("Clone shares memory with the original")
	}
}
