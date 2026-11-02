package config_test

import (
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// catalogHooks stands in for the catalog loader (CAT-01, not built yet).
func catalogHooks() *config.Hooks {
	return &config.Hooks{
		ProfileExists:   func(id string) bool { return id == "small-8gb" || id == "cpu-only" },
		CatalogModelIDs: func() []string { return []string{"qwen3.5-9b-q4km", "gemma-4b"} },
	}
}

type validationCase struct {
	name     string
	old, new string // swapped once in the example
	path     string // the problem's path
	code     string // the problem's code, when it has a specific one
	sub      string // a fragment of the message
	line     int    // expected line in the example (0: not checked)
}

// The table has at least one rejected value per row of agent.md 3.4.
var validationCases = []validationCase{
	{"schema_version 2", "schema_version: 1", "schema_version: 2", "schema_version", "", "must be 1", 1},
	{"schema_version 0", "schema_version: 1", "schema_version: 0", "schema_version", "", "must be 1", 1},
	{"node.id uppercase", "id: ai-n3 ", "id: AI-N3 ", "node.id", "", "must look like ai-n3", 4},
	{"node.id leading zero", "id: ai-n3 ", "id: ai-n03", "node.id", "", "must look like ai-n3", 4},
	{"node.id zero", "id: ai-n3 ", "id: ai-n0 ", "node.id", "", "must look like ai-n3", 4},
	{"node.id too many digits", "id: ai-n3 ", "id: ai-n12345", "node.id", "", "must look like ai-n3", 4},
	{"node.id other prefix", "id: ai-n3 ", "id: gpu-3 ", "node.id", "", "must look like ai-n3", 4},
	{"node.id empty", "id: ai-n3 ", `id: ""    `, "node.id", "", "required", 4},
	{"profile removed", "profile: small-8gb ", "profile: \"\"       ", "profile", "", "required", 0},
	{"llama.mode", "mode: router ", "mode: both   ", "llama.mode", "", "router or single", 0},
	{"llama.backend", "backend: cuda ", "backend: metal", "llama.backend", "", "cuda, vulkan or cpu", 0},
	{"llama.commit uppercase", "commit: f3586e66b ", "commit: F3586E66B ", "llama.commit", "", "lowercase hex", 0},
	{"llama.commit too short", "commit: f3586e66b ", "commit: f3586e   ", "llama.commit", "", "7 to 40", 0},
	{"llama.commit too long", "commit: f3586e66b ", "commit: " + strings.Repeat("a", 41), "llama.commit", "", "7 to 40", 0},
	{"llama.cuda_arch text", "cuda_arch: null ", `cuda_arch: "sm_75"`, "llama.cuda_arch", "", "75 or 75;86", 0},
	{"llama.cuda_arch comma", "cuda_arch: null ", `cuda_arch: "75,86"`, "llama.cuda_arch", "", "75 or 75;86", 0},
	{"llama.load_timeout_seconds low", "load_timeout_seconds: 300", "load_timeout_seconds: 29 ", "llama.load_timeout_seconds", "", "30 to 3600", 0},
	{"llama.load_timeout_seconds high", "load_timeout_seconds: 300", "load_timeout_seconds: 3601", "llama.load_timeout_seconds", "", "30 to 3600", 0},
	{"legacy_aliases alias too short", "[qwen3.5-9b]", "[q]", "llama.legacy_aliases.qwen3.5-9b-q4km[0]", "", "2 to 64", 0},
	{"legacy_aliases alias uppercase", "[qwen3.5-9b]", "[Qwen]", "llama.legacy_aliases.qwen3.5-9b-q4km[0]", "", "lowercase", 0},
	{"legacy_aliases alias is a catalog id", "[qwen3.5-9b]", "[gemma-4b]", "llama.legacy_aliases.qwen3.5-9b-q4km[0]", "", "must not equal a catalog model id", 0},
	{"legacy_aliases key not a catalog id", "qwen3.5-9b-q4km: [", "no-such-model: [", "llama.legacy_aliases.no-such-model", "", "model id from the catalog", 0},
	{"extra_env bad name", "extra_env: {}", "extra_env: {PATH: x}", "llama.extra_env.PATH", "", "GGML_, LLAMA_ or CUDA_", 0},
	{"extra_env lowercase name", "extra_env: {}", "extra_env: {ggml_x: x}", "llama.extra_env.ggml_x", "", "GGML_, LLAMA_ or CUDA_", 0},
	{"extra_env LLAMA_ARG refused", "extra_env: {}", "extra_env: {LLAMA_ARG_MODEL: x}", "llama.extra_env.LLAMA_ARG_MODEL", "", "LLAMA_ARG_*", 0},
	{"extra_env value too long", "extra_env: {}", "extra_env: {GGML_X: " + strings.Repeat("v", 257) + "}", "llama.extra_env.GGML_X", "", "limit is 256", 0},
	{"extra_env value not printable", "extra_env: {}", `extra_env: {GGML_X: "a\tb"}`, "llama.extra_env.GGML_X", "", "printable", 0},
	{"tailscale.serve_pattern", "serve_pattern: B ", "serve_pattern: C ", "tailscale.serve_pattern", "", "A, B or none", 0},
	{"cloud.url http", "url: https://ai.marin.cr ", "url: http://ai.marin.cr  ", "cloud.url", config.CodeCloudURLNotHTTPS, "https://", 0},
	{"cloud.url no scheme", "url: https://ai.marin.cr ", "url: ai.marin.cr         ", "cloud.url", config.CodeCloudURLNotHTTPS, "https://", 0},
	{"cloud.url ftp", "url: https://ai.marin.cr ", "url: ftp://ai.marin.cr   ", "cloud.url", config.CodeCloudURLNotHTTPS, "https://", 0},
	{"cloud.url loopback without dev_insecure", "url: https://ai.marin.cr ", "url: http://127.0.0.1:3535", "cloud.url", config.CodeCloudURLNotHTTPS, "dev_insecure", 0},
	{"cloud.url localhost with dev_insecure", "url: https://ai.marin.cr ", "url: http://localhost:3535", "cloud.url", config.CodeCloudURLNotHTTPS, "https://", 0},
	{"cloud.url loopback without port", "url: https://ai.marin.cr ", "url: http://127.0.0.1     ", "cloud.url", config.CodeCloudURLNotHTTPS, "https://", 0},
	{"cloud.url with a path", "url: https://ai.marin.cr ", "url: https://ai.marin.cr/api", "cloud.url", "", "without a path", 0},
	{"cloud.url with a query", "url: https://ai.marin.cr ", "url: https://ai.marin.cr?x=1", "cloud.url", "", "without a path", 0},
	{"cloud.url with a password", "url: https://ai.marin.cr ", "url: https://u:p@example.test", "cloud.url", "", "user name or password", 0},
	{"cloud.url without a host", "url: https://ai.marin.cr ", "url: https://            ", "cloud.url", "", "host name", 0},
	{"cloud.allow unknown", "catalog.refresh, agent.update", "catalog.refresh, agent.reboot", "cloud.allow[13]", "", "unknown job name", 0},
	{"cloud.allow with the localai prefix", "[status.refresh,", "[localai.status.refresh,", "cloud.allow[0]", "", "unknown job name", 0},
	{"cloud.concurrency low", "concurrency: 1  ", "concurrency: 0  ", "cloud.concurrency", "", "1 to 4", 0},
	{"cloud.concurrency high", "concurrency: 1  ", "concurrency: 5  ", "cloud.concurrency", "", "1 to 4", 0},
	{"cloud.poll.heartbeat_seconds low", "heartbeat_seconds: 15", "heartbeat_seconds: 4 ", "cloud.poll.heartbeat_seconds", "", "5 to 60", 0},
	{"cloud.poll.heartbeat_seconds high", "heartbeat_seconds: 15", "heartbeat_seconds: 61", "cloud.poll.heartbeat_seconds", "", "5 to 60", 0},
	{"cloud.poll.claim_seconds low", "claim_seconds: 3 ", "claim_seconds: 0 ", "cloud.poll.claim_seconds", "", "1 to 30", 0},
	{"cloud.poll.claim_seconds high", "claim_seconds: 3 ", "claim_seconds: 31", "cloud.poll.claim_seconds", "", "1 to 30", 0},
	{"cloud.poll.state_seconds low", "state_seconds: 60", "state_seconds: 14", "cloud.poll.state_seconds", "", "15 to 600", 0},
	{"cloud.poll.state_seconds high", "state_seconds: 60", "state_seconds: 601", "cloud.poll.state_seconds", "", "15 to 600", 0},
	{"ui.language", "language: auto                    # auto | en", "language: fr                      # auto | en", "ui.language", "", "auto, en or es", 0},
	{"ports.agent below 1024", "agent: 7070 ", "agent: 80   ", "ports.agent", "", "1024 to 65535", 0},
	{"ports.llama above 65535", "llama: 8080\n", "llama: 65536\n", "ports.llama", "", "1024 to 65535", 0},
	{"ports.kiwix zero", "kiwix: 8888", "kiwix: 0   ", "ports.kiwix", "", "1024 to 65535", 0},
	{"ports.whisper duplicates agent", "whisper: 8081", "whisper: 7070", "ports.whisper", "", "must differ from ports.agent", 0},
	{"ports.llama duplicates agent", "llama: 8080\n", "llama: 7070\n", "ports.llama", "", "must differ from ports.agent", 0},
	{"logging.level", "level: info   ", "level: trace  ", "logging.level", "", "debug, info, warn or error", 0},
}

func TestValidationRulesRejectOneBadValueEach(t *testing.T) {
	base := example(t)
	for _, tt := range validationCases {
		t.Run(tt.name, func(t *testing.T) {
			in := swap(t, base, tt.old, tt.new)
			if tt.name == "cloud.url localhost with dev_insecure" {
				// dev_insecure only legalises 127.0.0.1, not other http hosts.
				in = swap(t, in, "dev_insecure: false", "dev_insecure: true")
			}
			_, probs := config.Load([]byte(in), catalogHooks())
			p, ok := findProblem(probs, tt.path)
			if !ok {
				t.Fatalf("no problem at %q; got %+v", tt.path, probs)
			}
			if p.Severity != config.SeverityError {
				t.Errorf("want an error, got %q", p.Severity)
			}
			if p.Code != tt.code {
				t.Errorf("code = %q, want %q", p.Code, tt.code)
			}
			if !strings.Contains(p.Message, tt.sub) {
				t.Errorf("message %q lacks %q", p.Message, tt.sub)
			}
			if tt.line != 0 && p.Line != tt.line {
				t.Errorf("line = %d, want %d", p.Line, tt.line)
			}
			if n := len(probs.Errors()); n != 1 {
				t.Errorf("want exactly one error problem, got %d: %+v", n, probs)
			}
		})
	}
}

func TestEveryRowOfTheRuleTableIsCovered(t *testing.T) {
	rows := []string{
		"schema_version", "node.id", "llama.mode", "llama.backend", "llama.commit", "llama.cuda_arch",
		"llama.load_timeout_seconds", "llama.legacy_aliases", "llama.extra_env", "tailscale.serve_pattern",
		"cloud.url", "cloud.allow", "cloud.concurrency", "cloud.poll.heartbeat_seconds",
		"cloud.poll.claim_seconds", "cloud.poll.state_seconds", "ui.language", "ports.", "logging.level",
	}
	for _, row := range rows {
		found := false
		for _, c := range validationCases {
			if strings.HasPrefix(c.path, row) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no validation case for the %q rule", row)
		}
	}
}

func TestValidValuesAtTheEdgesAreAccepted(t *testing.T) {
	base := example(t)
	tests := []struct{ name, old, new string }{
		{"node.id max", "id: ai-n3 ", "id: ai-n9999"},
		{"cuda_arch list", "cuda_arch: null", `cuda_arch: "75;86;120"`},
		{"commit 40 hex", "commit: f3586e66b", "commit: " + strings.Repeat("a", 40)},
		{"commit 7 hex", "commit: f3586e66b", "commit: 1234567  "},
		{"timeout min", "load_timeout_seconds: 300", "load_timeout_seconds: 30 "},
		{"timeout max", "load_timeout_seconds: 300", "load_timeout_seconds: 3600"},
		{"env 256 chars", "extra_env: {}", "extra_env: {CUDA_X: " + strings.Repeat("v", 256) + "}"},
		{"env GGML", "extra_env: {}", `extra_env: {GGML_VK_SUBALLOCATION_BLOCK_SIZE: "4294967296"}`},
		{"serve_pattern none", "serve_pattern: B ", "serve_pattern: none"},
		{"loopback with dev_insecure", "dev_insecure: false", "dev_insecure: true "},
		{"concurrency 4", "concurrency: 1  ", "concurrency: 4  "},
		{"heartbeat 5", "heartbeat_seconds: 15", "heartbeat_seconds: 5 "},
		{"heartbeat 60", "heartbeat_seconds: 15", "heartbeat_seconds: 60"},
		{"claim 1 and 30", "claim_seconds: 3 ", "claim_seconds: 30"},
		{"state 15", "state_seconds: 60", "state_seconds: 15"},
		{"state 600", "state_seconds: 60", "state_seconds: 600"},
		{"ports edge", "llama: 8080\n", "llama: 1024\n"},
		{"ports top", "whisper: 8081", "whisper: 65535"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := swap(t, base, tt.old, tt.new)
			if tt.name == "loopback with dev_insecure" {
				in = swap(t, in, "url: https://ai.marin.cr ", "url: http://127.0.0.1:3535")
			}
			_, probs := config.Load([]byte(in), catalogHooks())
			if len(probs) != 0 {
				t.Fatalf("want no problems, got %+v", probs)
			}
		})
	}
}

func TestProfileMissingFromTheCatalogIsNotFatal(t *testing.T) {
	in := swap(t, example(t), "profile: small-8gb ", "profile: no-such-pr")
	cfg, probs := config.Load([]byte(in), catalogHooks())
	if probs.HasErrors() {
		t.Fatalf("a missing profile must not invalidate the file (the agent still starts): %+v", probs)
	}
	p, ok := findProblem(probs, "profile")
	if !ok || p.Code != config.CodeProfileMissing || p.Severity != config.SeverityWarning {
		t.Fatalf("want a %s warning on profile, got %+v", config.CodeProfileMissing, probs)
	}
	if cfg.Profile != "no-such-pr" {
		t.Errorf("the profile value must be kept: %q", cfg.Profile)
	}
}

func TestCatalogChecksAreSkippedWithoutACatalog(t *testing.T) {
	in := swap(t, example(t), "qwen3.5-9b-q4km: [", "anything-at-all: [")
	for name, h := range map[string]*config.Hooks{
		"nil hooks":      nil,
		"no model ids":   {},
		"empty catalog":  {CatalogModelIDs: func() []string { return nil }},
		"nil profile fn": {ProfileExists: nil},
	} {
		if _, probs := config.Load([]byte(in), h); len(probs) != 0 {
			t.Errorf("%s: an unloaded or empty catalog must not invalidate the file: %+v", name, probs)
		}
	}
}

func TestSeveralViolationsAreSeveralProblems(t *testing.T) {
	in := swap(t, example(t), "mode: router ", "mode: both   ")
	in = swap(t, in, "level: info   ", "level: trace  ")
	in = swap(t, in, "concurrency: 1  ", "concurrency: 9  ")
	_, probs := config.Load([]byte(in), catalogHooks())
	for _, path := range []string{"llama.mode", "logging.level", "cloud.concurrency"} {
		if _, ok := findProblem(probs, path); !ok {
			t.Errorf("missing problem for %s in %+v", path, probs)
		}
	}
	if len(probs.Errors()) != 3 {
		t.Errorf("want 3 problems, got %+v", probs)
	}
}

type fakeBlockValidator struct{ probs config.Problems }

func (f fakeBlockValidator) ValidateBlock(config.RawBlock) config.Problems { return f.probs }

func TestOwnedBlockValidatorsAreCalledAndPrefixed(t *testing.T) {
	h := &config.Hooks{Blocks: map[string]config.BlockValidator{
		"sync": fakeBlockValidator{config.Problems{{Path: "ark_drive.default_profile", Message: "unknown profile"}}},
		"maps": fakeBlockValidator{config.Problems{{Message: "bad block"}}},
	}}
	_, probs := config.Load([]byte(example(t)), h)
	p, ok := findProblem(probs, "sync.ark_drive.default_profile")
	if !ok || p.Severity != config.SeverityError || p.Line == 0 {
		t.Errorf("sync problem missing, wrong severity or no line: %+v", probs)
	}
	if _, ok := findProblem(probs, "maps"); !ok {
		t.Errorf("a block-level problem must carry the block name: %+v", probs)
	}
}

func TestOwnedBlocksAreNotValidatedWithoutAValidator(t *testing.T) {
	in := swap(t, example(t), "threads: 4", "threads: not-a-number")
	if _, probs := config.Load([]byte(in), nil); len(probs) != 0 {
		t.Errorf("owned blocks are parsed and kept, not validated, until their package registers: %+v", probs)
	}
}
