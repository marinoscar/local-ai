package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One failing catalog per lint rule (catalog.md section 9). Each case edits the valid fixture, so
// the case table doubles as documentation of what every rule rejects: the expected code, file and
// YAML path are exactly what a maintainer sees in the CI log. Every case also asserts the number of
// problems, so a rule never cascades into noise.

type want struct{ code, file, path string }

const (
	fM  = FileModels
	fA  = FileArk
	fV  = FileVersion
	pSm = "profiles/small-8gb.yaml"
	pCu = "profiles/cuda-12gb.yaml"
	pCp = "profiles/cpu-only.yaml"
	pAr = "profiles/ark-drive-full.yaml"
)

func w(code, file, path string) want { return want{code, file, path} }

func appendTo(file, text string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		writeFile(t, dir, file, readFile(t, dir, file)+text)
	}
}

func remove(rel string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
}

func put(rel, content string) func(*testing.T, string) {
	return func(t *testing.T, dir string) { writeFile(t, dir, rel, content) }
}

const shaD = `"@sha:d@"`

var ruleCases = []struct {
	name  string
	edits []edit
	setup func(*testing.T, string)
	want  []want
	msg   string // substring of one message
}{
	// ---- LAI-CAT-010 schema ----
	{name: "010 unknown key", edits: []edit{{fM, "    roles: [chat, reasoning, coder]\n", "    roles: [chat, reasoning, coder]\n    bogus: 1\n"}},
		want: []want{w(CodeSchema, fM, "models[0].bogus")}, msg: `unknown key "bogus"`},
	{name: "010 unknown nested key", edits: []edit{{fM, `mmproj_file_kind: mmproj}`, `mmproj_file_kind: mmproj, turbo: true}`}},
		want: []want{w(CodeSchema, fM, "models[2].llama.turbo")}},
	{name: "010 unknown translation key", edits: []edit{{fM, "summary_es:", "summary_ESP:"}},
		want: []want{w(CodeSchema, fM, "models[0].summary_ESP")}},
	{name: "010 unknown top-level key", edits: []edit{{fM, "models:\n  - id: model-alpha-9b-q4", "extra: 1\nmodels:\n  - id: model-alpha-9b-q4"}},
		want: []want{w(CodeSchema, fM, "extra")}},
	{name: "010 missing required field", edits: []edit{{fM, "    display_name: \"Model Gamma Vision\"\n", ""}},
		want: []want{w(CodeSchema, fM, "models[2].display_name")}, msg: "missing required field"},
	{name: "010 empty required string", edits: []edit{{fM, `display_name: "Model Gamma Vision"`, `display_name: ""`}},
		want: []want{w(CodeSchema, fM, "models[2].display_name")}, msg: "must not be empty"},
	{name: "010 wrong type", edits: []edit{{fM, "survival_tier: 2", `survival_tier: "two"`}},
		want: []want{w(CodeSchema, fM, "models[1].survival_tier")}, msg: "expected a whole number, got a string"},
	{name: "010 number where a string is expected", edits: []edit{{fM, "name: ggml-small.bin", "name: 12345"}},
		want: []want{w(CodeSchema, fM, "models[4].files[0].name")}, msg: "quote it"},
	{name: "010 list where a mapping is expected", edits: []edit{{fA, `pinned: {file: wiki_alpha_es_nopic_2026-09.zim, sha256: "@sha:b@"}`, `pinned: [x]`}},
		want: []want{w(CodeSchema, fA, "content[1].source.pinned")}, msg: "expected a mapping"},
	{name: "010 schema_version not 1", edits: []edit{{fM, "schema_version: 1\nmodels:", "schema_version: 2\nmodels:"}},
		want: []want{w(CodeSchema, fM, "schema_version")}, msg: "schema_version must be 1, got 2"},
	{name: "010 schema_version missing", edits: []edit{{fA, "schema_version: 1\ncontent:", "content:"}},
		want: []want{w(CodeSchema, fA, "schema_version")}},
	{name: "010 engine not in the enum", edits: []edit{{fM, "engine: whisper", "engine: tts"}},
		want: []want{w(CodeSchema, fM, "models[4].engine")}},
	{name: "010 summary over 120 characters", edits: []edit{{fM, `summary: "Small embedding model for offline search."`, `summary: "` + strings.Repeat("x", 121) + `"`}},
		want: []want{w(CodeSchema, fM, "models[3].summary")}, msg: "121 characters"},
	{name: "010 role not in the enum", edits: []edit{{fM, "roles: [embedding]", "roles: [embeding]"}},
		want: []want{w(CodeSchema, fM, "models[3].roles[0]")}},
	{name: "010 ctx zero", edits: []edit{{fM, "llama: {ctx: 2048, n_gpu_layers: 0}", "llama: {ctx: 0, n_gpu_layers: 0}"}},
		want: []want{w(CodeSchema, fM, "models[3].llama.ctx")}},
	{name: "010 n_gpu_layers neither all nor a number", edits: []edit{{fM, "llama: {ctx: 2048, n_gpu_layers: 0}", "llama: {ctx: 2048, n_gpu_layers: some}"}},
		want: []want{w(CodeSchema, fM, "models[3].llama.n_gpu_layers")}},
	{name: "010 flash_attn not in the enum", edits: []edit{{fM, `flash_attn: "off"`, `flash_attn: "sometimes"`}},
		want: []want{w(CodeSchema, fM, "models[2].llama.flash_attn")}},
	{name: "010 license neither SPDX nor custom url", edits: []edit{{fM, `license: "custom:https://example.test/licenses/beta"`, `license: "custom:"`}},
		want: []want{w(CodeSchema, fM, "models[1].license")}},
	{name: "010 backend not in the enum", edits: []edit{{fM, "requirements: {backends: [cpu], min_vram_mib: 0, min_ram_mib: 512", "requirements: {backends: [tpu], min_vram_mib: 0, min_ram_mib: 512"}},
		want: []want{w(CodeSchema, fM, "models[5].requirements.backends[0]")}},
	{name: "010 unsafe file name", edits: []edit{{fM, `name: "main/Model-Beta-27B-Q4_K_M.gguf"`, `name: "../Model-Beta.gguf"`}},
		want: []want{w(CodeSchema, fM, "models[1].files[0].name")}},
	{name: "010 url source needs a file url", edits: []edit{{fM, `url: "https://example.test/models/model-delta-embed-q8.gguf"`, "url: null"}},
		want: []want{w(CodeSchema, fM, "models[3].files[0].url")}},
	{name: "010 huggingface source needs a repo", edits: []edit{{fM, "      repo: org-alpha/whisper-small-ggml\n", ""}},
		want: []want{w(CodeSchema, fM, "models[4].source.repo")}},
	{name: "010 YAML alias", edits: []edit{{fM, `display_name: "Whisper Small"`, `display_name: &n "Whisper Small"`}, {fM, `display_name: "Piper English voice"`, `display_name: *n`}},
		want: []want{w(CodeSchema, fM, "models[5].display_name")}, msg: "aliases"},
	{name: "010 duplicate key", edits: []edit{{fM, `display_name: "Model Gamma Vision"`, "display_name: \"Model Gamma Vision\"\n    display_name: \"again\""}},
		want: []want{w(CodeSchema, fM, "models[2].display_name")}, msg: "duplicate key"},
	{name: "010 YAML syntax error", edits: []edit{{fM, "models:\n  - id: model-alpha-9b-q4", "models:\n  - id: [unterminated"}},
		want: []want{w(CodeSchema, fM, "")}, msg: "YAML syntax error"},
	{name: "010 empty file", setup: put(fA, ""), want: []want{w(CodeSchema, fA, "")}, msg: "file is empty"},
	{name: "010 comment-only file", setup: put(fA, "# nothing here\n"), want: []want{w(CodeSchema, fA, "")}},
	{name: "010 two YAML documents", setup: appendTo(fM, "---\nfoo: 1\n"), want: []want{w(CodeSchema, fM, "")}, msg: "only one YAML document"},
	{name: "010 top level is a list", setup: put(fM, "- a\n- b\n"), want: []want{w(CodeSchema, fM, "")}, msg: "expected a mapping"},
	{name: "010 kind not in the enum", edits: []edit{{fA, "kind: docs\n    survival_tier: 1", "kind: manual\n    survival_tier: 1"}},
		want: []want{w(CodeSchema, fA, "content[11].kind")}},
	{name: "010 field not used by the source type", edits: []edit{{fA, "      type: repo_docs", "      type: repo_docs\n      book: x"}},
		want: []want{w(CodeSchema, fA, "content[11].source.book")}},
	{name: "010 missing pmtiles field", edits: []edit{{fA, "      maxzoom: 15\n", ""}},
		want: []want{w(CodeSchema, fA, "content[3].source.maxzoom")}},
	{name: "010 profile unknown key", setup: appendTo(pSm, "extra: 1\n"), want: []want{w(CodeSchema, pSm, "extra")}},
	{name: "010 profile missing field", edits: []edit{{pSm, "display_name: \"Small GPU laptop (8 GB VRAM)\"\n", ""}},
		want: []want{w(CodeSchema, pSm, "display_name")}},
	{name: "010 profile storage target", edits: []edit{{pSm, "target: internal", "target: nas"}}, want: []want{w(CodeSchema, pSm, "storage.target")}},

	// ---- LAI-CAT-011 pinning ----
	{name: "011 revision is a floating ref", edits: []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}},
		want: []want{w(CodePinning, fM, "models[1].source.revision")}, msg: `revision must be a 40-hex commit, got "main"`},
	{name: "011 revision uppercase", edits: []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: FEDCBA9876543210FEDCBA9876543210FEDCBA98"}},
		want: []want{w(CodePinning, fM, "models[1].source.revision")}},
	{name: "011 revision missing", edits: []edit{{fM, "      repo: org-alpha/whisper-small-ggml\n      revision: 0123456789abcdef0123456789abcdef01234567\n", "      repo: org-alpha/whisper-small-ggml\n"}},
		want: []want{w(CodePinning, fM, "models[4].source.revision")}},
	{name: "011 sha256 too short", edits: []edit{{fM, `sha256: "@sha:2@"`, `sha256: "abc"`}},
		want: []want{w(CodePinning, fM, "models[1].files[0].sha256")}},
	{name: "011 sha256 uppercase", edits: []edit{{fM, `sha256: "@sha:2@"`, `sha256: "ABCDEF0123"`}},
		want: []want{w(CodePinning, fM, "models[1].files[0].sha256")}},
	{name: "011 sha256 missing", edits: []edit{{fM, `, sha256: "@sha:8@"}`, `}`}},
		want: []want{w(CodePinning, fM, "models[4].files[0].sha256")}},
	{name: "011 size_bytes missing", edits: []edit{{fM, "size_bytes: 487000000, ", ""}},
		want: []want{w(CodePinning, fM, "models[4].files[0].size_bytes")}},
	{name: "011 github_release sha256 invalid", edits: []edit{{fA, `sha256: "@sha:c@"`, `sha256: "nothex"`}},
		want: []want{w(CodePinning, fA, "content[4].source.sha256")}},
	{name: "011 pinned url without sha256", edits: []edit{{fA, "      sha256: " + shaD + "\n", ""}},
		want: []want{w(CodePinning, fA, "content[7].source.sha256")}},
	{name: "011 pinned kiwix sha256 invalid", edits: []edit{{fA, `sha256: "@sha:b@"`, `sha256: "xyz"`}},
		want: []want{w(CodePinning, fA, "content[1].source.pinned.sha256")}},

	// ---- LAI-CAT-012 ids ----
	{name: "012 id with a capital", setup: appendTo(fA, "  - id: Docs_Bad\n    kind: docs\n    source: {type: repo_docs}\n"),
		want: []want{w(CodeID, fA, "content[12].id")}},
	{name: "012 id too short", setup: appendTo(fA, "  - id: ab\n    kind: docs\n    source: {type: repo_docs}\n"),
		want: []want{w(CodeID, fA, "content[12].id")}},
	{name: "012 duplicate id inside ark", setup: appendTo(fA, "  - id: docs-bundle\n    kind: docs\n    source: {type: repo_docs}\n"),
		want: []want{w(CodeID, fA, "content[12].id")}, msg: `duplicate id "docs-bundle"`},
	{name: "012 id shared by a model and an ark item", setup: appendTo(fA, "  - id: whisper-small\n    kind: docs\n    source: {type: repo_docs}\n"),
		want: []want{w(CodeID, fA, "content[12].id")}, msg: "models.yaml:models[4].id"},
	{name: "012 duplicate model id", setup: appendTo(fM, `
  - id: whisper-small
    display_name: "Copy"
    summary: "Copy."
    engine: whisper
    roles: [stt]
    languages: [en]
    license: mit
    survival_tier: 3
    source: {type: url}
    files: [{kind: whisper, name: copy.bin, url: "https://example.test/copy.bin", size_bytes: 1, sha256: "@sha:1@"}]
    requirements: {backends: [cpu]}
`), want: []want{w(CodeID, fM, "models[6].id")}},
	{name: "012 profile id format and file name", edits: []edit{{pCp, "id: cpu-only", "id: CPU_Only"}},
		want: []want{w(CodeID, pCp, "id"), w(CodeLayout, pCp, "id")}},

	// ---- LAI-CAT-013 model file rules ----
	{name: "013 engine llama without a llama block", edits: []edit{{fM, "    llama: {ctx: 2048, n_gpu_layers: 0}\n", ""}},
		want: []want{w(CodeModelFiles, fM, "models[3].llama")}},
	{name: "013 engine llama without a weights file", edits: []edit{{fM, "- kind: weights\n        name: Model-Alpha-9B-Q4_K_M.gguf", "- kind: mmproj\n        name: Model-Alpha-9B-Q4_K_M.gguf"}},
		want: []want{w(CodeModelFiles, fM, "models[0].files")}},
	{name: "013 vision without mmproj_file_kind", edits: []edit{{fM, ", mmproj_file_kind: mmproj}", "}"}},
		want: []want{w(CodeModelFiles, fM, "models[2].llama.mmproj_file_kind")}},
	{name: "013 vision without an mmproj file", edits: []edit{{fM, `      - {kind: mmproj, name: mmproj-Model-Gamma-VL-f16.gguf, size_bytes: 900000000, sha256: "@sha:6@"}` + "\n", ""}},
		want: []want{w(CodeModelFiles, fM, "models[2].llama.mmproj_file_kind")}},
	{name: "013 draft without a draft file", edits: []edit{{fM, `      - {kind: draft, name: "Model-Beta-1B-Q4_K_M.gguf", size_bytes: 700000000, sha256: "@sha:3@"}` + "\n", ""}},
		want: []want{w(CodeModelFiles, fM, "models[1].llama.speculative")}},
	{name: "013 draft without draft_file_kind: draft", edits: []edit{{fM, "draft_file_kind: draft, draft_ngl: 99", "draft_file_kind: null, draft_ngl: 99"}},
		want: []want{w(CodeModelFiles, fM, "models[1].llama.speculative.draft_file_kind")}},
	{name: "013 draft-mtp names a file kind that is not there", edits: []edit{{fM, "draft_file_kind: null, draft_ngl: all}", "draft_file_kind: draft, draft_ngl: all}"}},
		want: []want{w(CodeModelFiles, fM, "models[0].llama.speculative.draft_file_kind")}},
	{name: "013 chat_template without a chat_template file", edits: []edit{{fM, `      - {kind: chat_template, name: "chat_template.jinja", size_bytes: 4096, sha256: "@sha:4@"}` + "\n", ""}},
		want: []want{w(CodeModelFiles, fM, "models[1].llama.chat_template")}},
	{name: "013 piper with only piper_onnx", edits: []edit{{fM, `      - {kind: piper_config, name: "en/voice-medium.onnx.json", size_bytes: 5000, sha256: "@sha:a@"}` + "\n", ""}},
		want: []want{w(CodeModelFiles, fM, "models[5].files")}, msg: "piper_config"},
	{name: "013 whisper with two whisper files", edits: []edit{{fM, `      - {kind: whisper, name: ggml-small.bin, size_bytes: 487000000, sha256: "@sha:8@"}` + "\n",
		`      - {kind: whisper, name: ggml-small.bin, size_bytes: 487000000, sha256: "@sha:8@"}` + "\n" +
			`      - {kind: whisper, name: ggml-small2.bin, size_bytes: 487000000, sha256: "@sha:8@"}` + "\n"}},
		want: []want{w(CodeModelFiles, fM, "models[4].files")}, msg: "exactly one"},

	// ---- LAI-CAT-014 extra_args and env ----
	{name: "014 extra_args --host", edits: []edit{{fM, `extra_args: ["--jinja", "--cache-type-k=q8_0"]`, `extra_args: ["--jinja", "--host", "0.0.0.0"]`}},
		want: []want{w(CodeExtraArgs, fM, "models[0].llama.extra_args[1]")}},
	{name: "014 extra_args --port=value", edits: []edit{{fM, `extra_args: ["--jinja", "--cache-type-k=q8_0"]`, `extra_args: ["--port=9000"]`}},
		want: []want{w(CodeExtraArgs, fM, "models[0].llama.extra_args[0]")}},
	{name: "014 env key is not a name", edits: []edit{{fM, `env: {GGML_VK_SUBALLOCATION_BLOCK_SIZE: "4294967296"}`, `env: {"BAD-KEY": "1"}`}},
		want: []want{w(CodeExtraArgs, fM, "models[0].llama.env.BAD-KEY")}},
	{name: "014 env value with a newline", edits: []edit{{fM, `"4294967296"}`, `"a\nb"}`}},
		want: []want{w(CodeExtraArgs, fM, "models[0].llama.env.GGML_VK_SUBALLOCATION_BLOCK_SIZE")}},

	// ---- LAI-CAT-015 ark item rules ----
	{name: "015 kiwix latest with refresh_days 10", edits: []edit{{fA, "refresh_days: 90", "refresh_days: 10"}},
		want: []want{w(CodeArkItem, fA, "content[0].refresh_days")}, msg: "refresh_days >= 30"},
	{name: "015 kiwix latest with refresh_days 29", edits: []edit{{fA, "    refresh_days: 30\n", "    refresh_days: 29\n"}},
		want: []want{w(CodeArkItem, fA, "content[2].refresh_days")}},
	{name: "015 kiwix latest without refresh_days", edits: []edit{{fA, "    refresh_days: 30\n", ""}},
		want: []want{w(CodeArkItem, fA, "content[2].refresh_days")}},
	{name: "015 kiwix pinned without a pinned block", edits: []edit{{fA, "version_policy: pinned, pinned: {file: wiki_alpha_es_nopic_2026-09.zim, sha256: \"@sha:b@\"}, checksum: pinned}", "version_policy: pinned, checksum: sidecar}"}},
		want: []want{w(CodeArkItem, fA, "content[1].source.pinned")}},
	{name: "015 bbox min above max", edits: []edit{{fA, "bbox: [-92.5, 7.0, -77.0, 18.6]", "bbox: [-77.0, 7.0, -92.5, 18.6]"}},
		want: []want{w(CodeArkItem, fA, "content[3].source.bbox")}},
	{name: "015 bbox with three numbers", edits: []edit{{fA, "bbox: [-92.5, 7.0, -77.0, 18.6]", "bbox: [-92.5, 7.0, -77.0]"}},
		want: []want{w(CodeArkItem, fA, "content[3].source.bbox")}},
	{name: "015 bbox outside the globe", edits: []edit{{fA, "bbox: [-92.5, 7.0, -77.0, 18.6]", "bbox: [-92.5, 7.0, -77.0, 95.0]"}},
		want: []want{w(CodeArkItem, fA, "content[3].source.bbox")}},
	{name: "015 kit item with tier 2", edits: []edit{{fA, "    survival_tier: 1\n    source:\n      type: git_mirror", "    survival_tier: 2\n    source:\n      type: git_mirror"}},
		want: []want{w(CodeArkItem, fA, "content[8].survival_tier")}},
	{name: "015 docs item with tier 3", edits: []edit{{fA, "kind: docs\n    survival_tier: 1", "kind: docs\n    survival_tier: 3"}},
		want: []want{w(CodeArkItem, fA, "content[11].survival_tier")}},
	{name: "015 kit-debs id disagrees with source.codename", edits: []edit{{fA, "codename: resolute\n      arch: amd64\n      packages: [build-essential", "codename: noble\n      arch: amd64\n      packages: [build-essential"}},
		want: []want{w(CodeArkItem, fA, "content[9].id")}},
	{name: "015 kit-nvidia id disagrees with source.arch", edits: []edit{{fA, "codename: resolute\n      arch: amd64\n      packages: [nvidia-driver", "codename: resolute\n      arch: arm64\n      packages: [nvidia-driver"}},
		want: []want{w(CodeArkItem, fA, "content[10].id")}},
	{name: "015 kit-debs id on a non-apt item", setup: appendTo(fA, "  - id: kit-debs-noble-amd64\n    kind: docs\n    source: {type: repo_docs}\n"),
		want: []want{w(CodeArkItem, fA, "content[12].id")}},
	{name: "015 source type not used by the kind", edits: []edit{{fA, "kind: kiwix\n    survival_tier: 2", "kind: maps\n    survival_tier: 2"}},
		want: []want{w(CodeArkItem, fA, "content[1].source.type"), w(CodePortalLinks, fA, "content[2].portal_links[0].es_item")}},

	// ---- LAI-CAT-016 optional fields ----
	{name: "016 extract on a kiwix item", edits: []edit{{fA, "      pinned: null\n", "      pinned: null\n      extract: true\n"}},
		want: []want{w(CodeOptionalField, fA, "content[0].source.extract")}, msg: "github_release and url"},
	{name: "016 extra_repos on a url item", edits: []edit{{fA, `      sidecar_url: "https://releases.example.test/SHA256SUMS"`, `      sidecar_url: "https://releases.example.test/SHA256SUMS"` + "\n      extra_repos: []"}},
		want: []want{w(CodeOptionalField, fA, "content[6].source.extra_repos")}},
	{name: "016 extra_repos key is not an embedded keyring", edits: []edit{{fA, "key: tailscale-archive-keyring.gpg", "key: made-up.gpg"}},
		want: []want{w(CodeOptionalField, fA, "content[9].source.extra_repos[0].key")}},
	{name: "016 extra_repos duplicate name", edits: []edit{{fA, "name: nvidia-cuda", "name: tailscale"}},
		want: []want{w(CodeOptionalField, fA, "content[9].source.extra_repos[1].name")}},
	{name: "016 checksum computed without trust", edits: []edit{{fA, "      trust: tofu\n", ""}},
		want: []want{w(CodeOptionalField, fA, "content[5].source.trust")}},
	{name: "016 trust tofu on a kit item", edits: []edit{{fA, "      sha256: " + shaD + "\n", "      checksum: computed\n      trust: tofu\n"}},
		want: []want{w(CodeOptionalField, fA, "content[7].source.trust")}, msg: "kind: maps"},
	{name: "016 trust without checksum computed", edits: []edit{{fA, "      sha256: " + shaD + "\n", "      sha256: " + shaD + "\n      trust: tofu\n"}},
		want: []want{w(CodeOptionalField, fA, "content[7].source.trust")}},
	{name: "016 url and files together", edits: []edit{{fA, "      sha256: " + shaD + "\n", "      sha256: " + shaD + "\n      files: [{url: \"https://data.example.test/a.zip\", sha256: " + shaD + "}]\n"}},
		want: []want{w(CodeOptionalField, fA, "content[7].source.files"), w(CodeOptionalField, fA, "content[7].source.sha256")}},
	{name: "016 url without url or files", edits: []edit{{fA, "      url: \"https://data.example.test/tool-bundle.tar.gz\"\n      sha256: " + shaD + "\n", ""}},
		want: []want{w(CodeOptionalField, fA, "content[7].source")}},
	{name: "016 checksum sidecar without sidecar_url", edits: []edit{{fA, "      sidecar_url: \"https://releases.example.test/SHA256SUMS\"\n", ""}},
		want: []want{w(CodeOptionalField, fA, "content[6].source.sidecar_url")}},
	{name: "016 sidecar_url without checksum sidecar", edits: []edit{{fA, "      sha256: " + shaD + "\n", "      sha256: " + shaD + "\n      sidecar_url: \"https://data.example.test/SUMS\"\n"}},
		want: []want{w(CodeOptionalField, fA, "content[7].source.sidecar_url")}},
	{name: "016 pmtiles checksum must be computed", edits: []edit{{fA, "      checksum: computed\n    expected_size_bytes: 2500000000", "      checksum: pinned\n    expected_size_bytes: 2500000000"}},
		want: []want{w(CodeOptionalField, fA, "content[3].source.checksum")}},
	{name: "016 trust must be tofu", edits: []edit{{fA, "trust: tofu", "trust: always"}},
		want: []want{w(CodeOptionalField, fA, "content[5].source.trust")}},

	// ---- LAI-CAT-017 portal_links ----
	{name: "017 portal_links on a non-kiwix item", edits: []edit{{fA, "      extract: true\n", "      extract: true\n    portal_links:\n      - {key: k, title: {en: a, es: b}, article: x, order: 1}\n"}},
		want: []want{w(CodePortalLinks, fA, "content[4].portal_links")}},
	{name: "017 duplicate key", edits: []edit{{fA, "key: burns", "key: bleeding"}},
		want: []want{w(CodePortalLinks, fA, "content[2].portal_links[1].key")}},
	{name: "017 key missing", edits: []edit{{fA, "      - key: burns\n        title", "      - title"}},
		want: []want{w(CodePortalLinks, fA, "content[2].portal_links[1].key")}},
	{name: "017 empty article", edits: []edit{{fA, `article: "Burns"`, `article: ""`}},
		want: []want{w(CodePortalLinks, fA, "content[2].portal_links[1].article")}},
	{name: "017 es_item is not in ark.yaml", edits: []edit{{fA, "es_item: wiki-alpha-es", "es_item: nope-item"}},
		want: []want{w(CodePortalLinks, fA, "content[2].portal_links[0].es_item")}},
	{name: "017 es_item is not a kiwix item", edits: []edit{{fA, "es_item: wiki-alpha-es", "es_item: map-region-alpha"}},
		want: []want{w(CodePortalLinks, fA, "content[2].portal_links[0].es_item")}},
	{name: "017 es_item without es_article", edits: []edit{{fA, "        es_article: \"Hemorragia\"\n", ""}},
		want: []want{w(CodePortalLinks, fA, "content[2].portal_links[0].es_article")}},
	{name: "017 es_article without es_item", edits: []edit{{fA, "        es_item: wiki-alpha-es\n", ""}},
		want: []want{w(CodePortalLinks, fA, "content[2].portal_links[0].es_article")}},
	{name: "010 portal link with article_es (dropped field)", edits: []edit{{fA, `es_article: "Hemorragia"`, `article_es: "Hemorragia"`}},
		want: []want{w(CodeSchema, fA, "content[2].portal_links[0].article_es")}, msg: `unknown key "article_es"`},

	// ---- LAI-CAT-018 profiles ----
	{name: "018 models_max 0", edits: []edit{{pSm, "models_max: 1", "models_max: 0"}},
		want: []want{w(CodeProfile, pSm, "router.models_max")}},
	{name: "018 autoload true", edits: []edit{{pSm, "autoload: false", "autoload: true"}},
		want: []want{w(CodeProfile, pSm, "router.autoload")}},
	{name: "018 default_model outside include", edits: []edit{{pSm, "\ndefault_model: model-alpha-9b-q4\n", "\ndefault_model: model-beta-27b-q4\n"}},
		want: []want{w(CodeProfile, pSm, "default_model")}},
	{name: "018 survival.models outside include", edits: []edit{{pSm, "  models: [model-alpha-9b-q4, model-gamma-vl-q4]\n  default_model", "  models: [model-alpha-9b-q4, model-beta-27b-q4]\n  default_model"}},
		want: []want{w(CodeProfile, pSm, "survival.models[1]")}},
	{name: "018 survival.default_model outside include", edits: []edit{{pSm, "  default_model: model-alpha-9b-q4\n  require_services", "  default_model: model-beta-27b-q4\n  require_services"}},
		want: []want{w(CodeProfile, pSm, "survival.default_model")}},
	{name: "018 ark-drive-full has a match", edits: []edit{{pAr, "router:", "match: {backends: [cuda]}\nrouter:"}},
		want: []want{w(CodeProfile, pAr, "match")}},
	{name: "018 node profile without a match", edits: []edit{{pSm, "match:\n  backends: [cuda]\n  vram_mib: {min: 7000, max: 11999}\n  ram_mib: {min: 12000}\n", ""}},
		want: []want{w(CodeProfile, pSm, "match")}},
	{name: "018 overlapping match ranges", edits: []edit{{pCu, "vram_mib: {min: 12000, max: 24999}", "vram_mib: {min: 8000, max: 24999}"}},
		want: []want{w(CodeProfile, pSm, "match")}, msg: `overlaps profile "cuda-12gb"`},
	{name: "018 overlap through open ranges", edits: []edit{{pCu, "vram_mib: {min: 12000, max: 24999}\n  ram_mib: {min: 12000}", "ram_mib: {min: 12000}"}},
		want: []want{w(CodeProfile, pSm, "match")}},
	{name: "018 range min above max", edits: []edit{{pSm, "vram_mib: {min: 7000, max: 11999}", "vram_mib: {min: 9000, max: 8000}"}},
		want: []want{w(CodeProfile, pSm, "match.vram_mib")}},

	// ---- LAI-CAT-004 cross references ----
	{name: "004 unknown model in include", edits: []edit{{pSm, ", whisper-small, piper-en-medium]\n  content: [wiki-alpha-en", ", whisper-tiny, piper-en-medium]\n  content: [wiki-alpha-en"}},
		want: []want{w(CodeUnknownRef, pSm, "include.models[3]")}},
	{name: "004 unknown content in include", edits: []edit{{pSm, "content: [wiki-alpha-en, medical-alpha-en,", "content: [wiki-alpha-xx, medical-alpha-en,"}},
		want: []want{w(CodeUnknownRef, pSm, "include.content[0]")}},
	{name: "004 model id listed as content", edits: []edit{{pSm, "content: [wiki-alpha-en, medical-alpha-en,", "content: [whisper-small, medical-alpha-en,"}},
		want: []want{w(CodeUnknownRef, pSm, "include.content[0]")}, msg: "it is a model id"},
	{name: "004 unknown default_model", edits: []edit{{pSm, "\ndefault_model: model-alpha-9b-q4\n", "\ndefault_model: no-such-model\n"}},
		want: []want{w(CodeUnknownRef, pSm, "default_model")}},
	{name: "004 unknown survival model", edits: []edit{{pCu, "  models: [model-alpha-9b-q4]\n  default_model: model-alpha-9b-q4\n  require", "  models: [no-such-model]\n  default_model: model-alpha-9b-q4\n  require"}},
		want: []want{w(CodeUnknownRef, pCu, "survival.models[0]")}},
	{name: "004 content id removed from ark.yaml", edits: []edit{{fA, "  - id: docs-bundle\n    kind: docs\n    survival_tier: 1\n    source:\n      type: repo_docs\n", ""}},
		want: []want{
			w(CodeUnknownRef, pSm, "include.content[7]"), w(CodeUnknownRef, pCu, "include.content[7]"),
			w(CodeUnknownRef, "profiles/max-128gb.yaml", "include.content[9]"), w(CodeUnknownRef, pCp, "include.content[2]"),
			w(CodeUnknownRef, pAr, "include.content[11]")}},

	// ---- LAI-CAT-019 layout and CATALOG_VERSION ----
	{name: "019 CATALOG_VERSION missing", setup: remove(fV), want: []want{w(CodeLayout, fV, "")}},
	{name: "019 CATALOG_VERSION empty", setup: put(fV, ""), want: []want{w(CodeLayout, fV, "")}},
	{name: "019 CATALOG_VERSION dashed date", setup: put(fV, "2026-11-02\n"), want: []want{w(CodeLayout, fV, "")}},
	{name: "019 CATALOG_VERSION N is zero", setup: put(fV, "2026.11.02-0\n"), want: []want{w(CodeLayout, fV, "")}},
	{name: "019 CATALOG_VERSION not a date", setup: put(fV, "2026.02.30-1\n"), want: []want{w(CodeLayout, fV, "")}},
	{name: "019 CATALOG_VERSION two lines", setup: put(fV, "2026.11.02-1\n2026.11.02-2\n"), want: []want{w(CodeLayout, fV, "")}},
	{name: "019 CATALOG_VERSION with a prefix", setup: put(fV, "catalog-2026.11.02-1\n"), want: []want{w(CodeLayout, fV, "")}},
	{name: "019 models.yaml missing", setup: remove(fM), want: []want{w(CodeLayout, fM, "")}},
	{name: "019 ark.yaml is a directory", setup: func(t *testing.T, dir string) {
		remove(fA)(t, dir)
		if err := os.Mkdir(filepath.Join(dir, fA), 0o755); err != nil {
			t.Fatal(err)
		}
	}, want: []want{w(CodeLayout, fA, "")}},
	{name: "019 profiles directory missing", setup: remove("profiles"), want: []want{w(CodeLayout, "profiles", "")}},
	{name: "019 profiles directory empty", setup: func(t *testing.T, dir string) {
		remove("profiles")(t, dir)
		if err := os.Mkdir(filepath.Join(dir, "profiles"), 0o755); err != nil {
			t.Fatal(err)
		}
	}, want: []want{w(CodeLayout, "profiles", "")}},
	{name: "019 profile file with the wrong extension", setup: put("profiles/notes.txt", "x"), want: []want{w(CodeLayout, "profiles/notes.txt", "")}},
	{name: "019 profile file .yml", setup: put("profiles/extra.yml", "x"), want: []want{w(CodeLayout, "profiles/extra.yml", "")}},
	{name: "019 directory inside profiles", setup: func(t *testing.T, dir string) {
		if err := os.Mkdir(filepath.Join(dir, "profiles", "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}, want: []want{w(CodeLayout, "profiles/sub", "")}},
	{name: "019 profile id differs from the file name", edits: []edit{{pCp, "id: cpu-only", "id: cpu-lite"}},
		want: []want{w(CodeLayout, pCp, "id")}},
	{name: "019 file over the size cap", setup: func(t *testing.T, dir string) {
		writeFile(t, dir, fM, strings.Repeat("# padding\n", MaxFileBytes/10+10))
	}, want: []want{w(CodeLayout, fM, "")}, msg: "larger than"},
}

func TestLintRules(t *testing.T) {
	for _, c := range ruleCases {
		if c.want == nil {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			dir := validDir(t)
			applyEdits(t, dir, c.edits)
			if c.setup != nil {
				c.setup(t, dir)
			}
			rep := Lint(dir, LintOptions{})
			if len(rep.Problems) != len(c.want) {
				t.Fatalf("want %d problem(s), got %d:\n%s", len(c.want), len(rep.Problems), problemsText(rep))
			}
			for _, wnt := range c.want {
				if !has(rep, wnt.code, wnt.file, wnt.path) {
					t.Errorf("missing %s %s:%s in:\n%s", wnt.code, wnt.file, wnt.path, problemsText(rep))
				}
			}
			if c.msg != "" {
				found := false
				for _, p := range rep.Problems {
					found = found || strings.Contains(p.Message, c.msg)
				}
				if !found {
					t.Errorf("no message contains %q in:\n%s", c.msg, problemsText(rep))
				}
			}
			if _, err := Load(dir); err == nil {
				t.Error("Load accepted a catalog that fails lint")
			}
		})
	}
}

// Edits that keep the catalog valid: the rules must not reject what the spec allows.
func TestLintAllowed(t *testing.T) {
	cases := []struct {
		name  string
		edits []edit
		setup func(*testing.T, string)
	}{
		{name: "TEST marker and pins.json are ignored", setup: func(t *testing.T, dir string) {
			writeFile(t, dir, FileTestMarker, "")
			writeFile(t, dir, "pins.json", "{}")
		}},
		{name: "kit-catalog is a kit item with a repo_docs source", setup: appendTo(fA, "  - id: kit-catalog\n    kind: kit\n    source: {type: repo_docs}\n")},
		{name: "CATALOG_VERSION without trailing newline", setup: put(fV, "2026.11.02-1")},
		{name: "CATALOG_VERSION with CRLF", setup: put(fV, "2026.11.02-1\r\n")},
		{name: "dotfile in profiles is ignored", setup: put("profiles/.keep", "")},
		{name: "profiles on different backends may share ranges",
			edits: []edit{{pCu, "backends: [cuda]\n  vram_mib: {min: 12000, max: 24999}", "backends: [vulkan]\n  vram_mib: {min: 8000, max: 24999}"}}},
		{name: "cpu-only is the catch-all and may overlap", edits: []edit{{pCp, "backends: [cpu]", "backends: [cuda, vulkan, cpu]"}}},
		{name: "kiwix item without survival_tier", edits: []edit{{fA, "    survival_tier: 2\n    source: {type: kiwix, book: wiki_alpha_es_nopic", "    source: {type: kiwix, book: wiki_alpha_es_nopic"}}},
		{name: "n_gpu_layers all", edits: []edit{{fM, "n_gpu_layers: 0}", "n_gpu_layers: all}"}}},
		{name: "extra_args that look like forbidden ones but are not", edits: []edit{{fM, `extra_args: ["--jinja", "--cache-type-k=q8_0"]`, `extra_args: ["--jinja", "--hostile", "-c", "4096", "--model-draft"]`}}},
		{name: "unquoted flash_attn on", edits: []edit{{fM, `flash_attn: "on"`, `flash_attn: on`}}},
		{name: "model url source with a pinned revision", edits: []edit{{fM, "    source:\n      type: url\n", "    source:\n      type: url\n      revision: 0123456789abcdef0123456789abcdef01234567\n"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := validDir(t)
			applyEdits(t, dir, c.edits)
			if c.setup != nil {
				c.setup(t, dir)
			}
			if rep := Lint(dir, LintOptions{}); !rep.Valid() {
				t.Fatalf("expected valid, got:\n%s", problemsText(rep))
			}
		})
	}
}

func TestForbiddenArgs(t *testing.T) {
	bad := []string{"--host", "--host=0.0.0.0", "--host 0.0.0.0", "--port", "--port=1", "--api-key", "--api-key=abc", "--api-key-file", "--api-key-file=/x",
		"-m", "-m=x", "--model", "--model=x", "--models-max", "--models-dir=/x", "--models-memory-margin", "--alias", "--alias=x", "-hf", "-hf=a/b"}
	good := []string{"--jinja", "-c", "--cache-type-k=q8_0", "--hostile", "--portable", "--model-draft", "-ngl", "--mlock", "-mg"}
	for _, a := range bad {
		if _, ok := forbiddenArg(a); !ok {
			t.Errorf("%q must be forbidden", a)
		}
	}
	for _, a := range good {
		if f, ok := forbiddenArg(a); ok {
			t.Errorf("%q must be allowed, flagged as %s", a, f)
		}
	}
}

func TestUnknownKeysAreRejectedEverywhere(t *testing.T) {
	// The issue's acceptance criterion: unknown keys are rejected with LAI-CAT-010, in all three file kinds.
	for _, c := range []struct{ file, old, new, path string }{
		{fM, "    engine: llama\n    roles: [chat, reasoning, coder]", "    engine: llama\n    surprise: yes\n    roles: [chat, reasoning, coder]", "models[0].surprise"},
		{fA, "    kind: docs\n    survival_tier: 1", "    kind: docs\n    surprise: yes\n    survival_tier: 1", "content[11].surprise"},
		{pSm, "router:\n  models_max: 1", "router:\n  surprise: yes\n  models_max: 1", "router.surprise"},
	} {
		dir := validDir(t)
		applyEdits(t, dir, []edit{{c.file, c.old, c.new}})
		rep := Lint(dir, LintOptions{})
		if len(rep.Problems) != 1 || !has(rep, CodeSchema, c.file, c.path) {
			t.Errorf("%s: %s", c.file, problemsText(rep))
		}
		if rep.Problems[0].Line == 0 {
			t.Errorf("%s: unknown key has no line number", c.file)
		}
	}
}

func TestLineNumbers(t *testing.T) {
	dir := validDir(t)
	applyEdits(t, dir, []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}})
	rep := Lint(dir, LintOptions{})
	if len(rep.Problems) != 1 {
		t.Fatal(problemsText(rep))
	}
	lines := strings.Split(readFile(t, dir, fM), "\n")
	got := rep.Problems[0].Line
	if got < 1 || got > len(lines) || !strings.Contains(lines[got-1], "revision: main") {
		t.Errorf("line %d does not point at the revision: %q", got, lines[got-1])
	}
}

func TestProblemsAreOrderedAndDeduplicated(t *testing.T) {
	dir := validDir(t)
	applyEdits(t, dir, []edit{
		{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"},
		{fM, "survival_tier: 2", "survival_tier: 9"},
		{pSm, "models_max: 1", "models_max: 0"},
	})
	writeFile(t, dir, fV, "bad\n")
	rep := Lint(dir, LintOptions{})
	var order []string
	for _, p := range rep.Problems {
		order = append(order, p.File+":"+p.Path)
	}
	wantOrder := []string{"CATALOG_VERSION:", "models.yaml:models[1].survival_tier", "models.yaml:models[1].source.revision", "profiles/small-8gb.yaml:router.models_max"}
	if strings.Join(order, "|") != strings.Join(wantOrder, "|") {
		t.Errorf("order:\n got %v\nwant %v", order, wantOrder)
	}
}
