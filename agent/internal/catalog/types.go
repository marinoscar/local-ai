// Package catalog is the one way the agent, aictl and CI read the catalog (docs/specs/catalog.md):
// typed schemas for models.yaml, ark.yaml and profiles/*.yaml, a strict loader (unknown keys are
// rejected with a file and a YAML path), the lint rules LAI-CAT-010..019, the source priority
// (Fetcher) and the atomic install into /etc/localai/catalog/.
//
// The catalog is data, not code, and a node that has been offline for years relies on its local
// copy to know what it has and how to verify it. So the rule that shapes this package is: a catalog
// that fails lint is never loaded and never installed; the previous one stays.
package catalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only schema_version of models.yaml, ark.yaml and the profiles.
const SchemaVersion = 1

// Names of the files of a catalog directory (catalog.md section 1).
const (
	FileModels  = "models.yaml"
	FileArk     = "ark.yaml"
	FileVersion = "CATALOG_VERSION"
	DirProfiles = "profiles"
	// FileTestMarker is written into /etc/localai/catalog/ by `aictl catalog use --test`.
	FileTestMarker = "TEST"
)

// Enumerations of the schema. They are plain slices so the lint rules and the tests share them.
var (
	engines        = []string{"llama", "whisper", "piper", "acestep"}
	roles          = []string{"chat", "reasoning", "coder", "vision", "embedding", "stt", "tts", "music"}
	modelSrcTypes  = []string{"huggingface", "url"}
	fileKinds      = []string{"weights", "mmproj", "draft", "chat_template", "whisper", "piper_onnx", "piper_config", "acestep"}
	backends       = []string{"cuda", "vulkan", "cpu"}
	flashAttn      = []string{"on", "off", "auto"}
	specTypes      = []string{"draft-mtp", "draft"}
	contentKinds   = []string{"kiwix", "maps", "kit", "docs"}
	arkSrcTypes    = []string{"kiwix", "url", "pmtiles_extract", "github_release", "git_mirror", "apt_closure", "repo_docs"}
	versionPolicy  = []string{"latest", "pinned"}
	kiwixChecksums = []string{"sidecar", "pinned"}
	urlChecksums   = []string{"pinned", "sidecar", "computed"}
	storageTargets = []string{"internal", "ark_drive"}
	archs          = []string{"amd64", "arm64"}
)

// The music engine (E11, catalog.md section 2): engine acestep, role music, file kind acestep.
const (
	EngineLlama     = "llama"
	EngineAcestep   = "acestep"
	RoleMusic       = "music"
	FileKindAcestep = "acestep"
)

// AcestepBackends are the backends an acestep model may declare. Only cuda is supported until the
// ACE-Step spike measures the others (TODO(#505)).
var AcestepBackends = []string{"cuda"}

// EmbeddedKeyrings are the keyring file names embedded in the agent (rebuild-kit.md section 3.4).
// An `extra_repos[].key` must be one of them; the sync engine uses the same list, so adding a
// keyring means editing this one constant.
var EmbeddedKeyrings = []string{
	"ubuntu-archive-keyring.gpg",
	"tailscale-archive-keyring.gpg",
	"nvidia-cuda-archive-keyring.gpg",
}

// ArkDriveProfileID is the profile that has no `match` and is never auto-selected.
const ArkDriveProfileID = "ark-drive-full"

// CPUOnlyProfileID is the emergency fallback, always evaluated last.
const CPUOnlyProfileID = "cpu-only"

// IntOrAll is a YAML value that is either the word `all` or a whole number >= 0
// (llama.n_gpu_layers, speculative.draft_ngl).
type IntOrAll struct {
	Set bool
	All bool
	N   int
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (v *IntOrAll) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return &yaml.TypeError{Errors: []string{`expected "all" or a whole number`}}
	}
	switch n.ShortTag() {
	case "!!null":
		return nil
	case "!!str":
		if n.Value == "all" {
			*v = IntOrAll{Set: true, All: true}
			return nil
		}
	case "!!int":
		i, err := strconv.Atoi(n.Value)
		if err == nil && i >= 0 {
			*v = IntOrAll{Set: true, N: i}
			return nil
		}
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf(`expected "all" or a whole number >= 0, got %q`, n.Value)}}
}

// ModelsFile is models.yaml (catalog.md section 2).
type ModelsFile struct {
	SchemaVersion int     `yaml:"schema_version" catalog:"required"`
	Models        []Model `yaml:"models" catalog:"required"`
}

// Model is one entry of models.yaml.
type Model struct {
	ID          string `yaml:"id" catalog:"required"`
	DisplayName string `yaml:"display_name" catalog:"required"`
	Summary     string `yaml:"summary" catalog:"required"`
	// Summaries holds the optional translations summary_<lang>, keyed by the full key name.
	Summaries    map[string]string `yaml:",inline" catalog:"keys=^summary_[a-z]{2,8}$"`
	Engine       string            `yaml:"engine" catalog:"required"`
	Roles        []string          `yaml:"roles" catalog:"required"`
	Languages    []string          `yaml:"languages" catalog:"required"`
	License      string            `yaml:"license" catalog:"required"`
	SurvivalTier int               `yaml:"survival_tier" catalog:"required"`
	Source       ModelSource       `yaml:"source" catalog:"required"`
	Files        []ModelFile       `yaml:"files" catalog:"required"`
	Requirements Requirements      `yaml:"requirements" catalog:"required"`
	Llama        *Llama            `yaml:"llama"`
	ClientHints  *ClientHints      `yaml:"client_hints"`
}

// ModelSource says where a model's files come from.
type ModelSource struct {
	Type     string `yaml:"type" catalog:"required"`
	Repo     string `yaml:"repo"`
	Revision string `yaml:"revision"`
}

// ModelFile is one file of a model. SizeBytes and SHA256 are not "required" for the schema: their
// absence is a pinning problem (LAI-CAT-011).
type ModelFile struct {
	Kind      string `yaml:"kind" catalog:"required"`
	Name      string `yaml:"name" catalog:"required"`
	URL       string `yaml:"url"`
	SizeBytes int64  `yaml:"size_bytes"`
	SHA256    string `yaml:"sha256"`
}

// Requirements says where a model may run and what it needs.
type Requirements struct {
	Backends     []string `yaml:"backends" catalog:"required"`
	MinVRAMMiB   int64    `yaml:"min_vram_mib"`
	MinRAMMiB    int64    `yaml:"min_ram_mib"`
	MinDiskBytes int64    `yaml:"min_disk_bytes"`
}

// Llama is the router preset of an engine: llama model (llama-router.md section 4.2).
type Llama struct {
	Ctx            int               `yaml:"ctx" catalog:"required"`
	NGPULayers     IntOrAll          `yaml:"n_gpu_layers"`
	FlashAttn      string            `yaml:"flash_attn"`
	Parallel       int               `yaml:"parallel"`
	Speculative    *Speculative      `yaml:"speculative"`
	MmprojFileKind string            `yaml:"mmproj_file_kind"`
	ChatTemplate   string            `yaml:"chat_template"`
	ExtraArgs      []string          `yaml:"extra_args"`
	Env            map[string]string `yaml:"env"`
}

// Speculative configures speculative decoding.
type Speculative struct {
	Type          string   `yaml:"type" catalog:"required"`
	NMax          int      `yaml:"n_max"`
	DraftFileKind string   `yaml:"draft_file_kind"`
	DraftNGL      IntOrAll `yaml:"draft_ngl"`
}

// ClientHints are used by `aictl key show` snippets and the gateway /v1/models.
type ClientHints struct {
	MaxOutputTokens int  `yaml:"max_output_tokens"`
	Reasoning       bool `yaml:"reasoning"`
}

// ArkFile is ark.yaml (catalog.md section 3).
type ArkFile struct {
	SchemaVersion int           `yaml:"schema_version" catalog:"required"`
	Content       []ContentItem `yaml:"content" catalog:"required"`
}

// ContentItem is one entry of ark.yaml.
type ContentItem struct {
	ID          string `yaml:"id" catalog:"required"`
	Kind        string `yaml:"kind" catalog:"required"`
	DisplayName string `yaml:"display_name"`
	Summary     string `yaml:"summary"`
	// Summaries holds the optional translations summary_<lang>, keyed by the full key name.
	Summaries         map[string]string `yaml:",inline" catalog:"keys=^summary_[a-z]{2,8}$"`
	SurvivalTier      int               `yaml:"survival_tier"`
	Languages         []string          `yaml:"languages"`
	License           string            `yaml:"license"`
	Source            ContentSource     `yaml:"source" catalog:"required"`
	ExpectedSizeBytes int64             `yaml:"expected_size_bytes"`
	RefreshDays       int               `yaml:"refresh_days"`
	PortalLinks       []PortalLink      `yaml:"portal_links"`
}

// Tier is the survival tier of the item: the declared one, else 1 for kit and docs items (they are
// always tier 1) and 3 for everything else (the first to be dropped by the budget planner).
func (c ContentItem) Tier() int {
	if c.SurvivalTier != 0 {
		return c.SurvivalTier
	}
	if c.Kind == "kit" || c.Kind == "docs" {
		return 1
	}
	return 3
}

// ContentSource is the union of the source types of catalog.md section 3. Which fields are allowed
// and required depends on Type (checked by the lint rules).
type ContentSource struct {
	Type string `yaml:"type" catalog:"required"`

	// kiwix
	Book          string       `yaml:"book"`
	BaseURL       string       `yaml:"base_url"`
	VersionPolicy string       `yaml:"version_policy"`
	Pinned        *KiwixPinned `yaml:"pinned"`

	// kiwix, url, pmtiles_extract
	Checksum string `yaml:"checksum"`

	// url (and git_mirror: URL only)
	URL        string    `yaml:"url"`
	SHA256     string    `yaml:"sha256"`
	Files      []URLFile `yaml:"files"`
	SidecarURL string    `yaml:"sidecar_url"`
	Trust      string    `yaml:"trust"`

	// url, github_release
	Extract *bool `yaml:"extract"`

	// pmtiles_extract
	PlanetURLPattern string    `yaml:"planet_url_pattern"`
	BBox             []float64 `yaml:"bbox"`
	MaxZoom          *int      `yaml:"maxzoom"`

	// github_release
	Repo  string `yaml:"repo"`
	Tag   string `yaml:"tag"`
	Asset string `yaml:"asset"`

	// git_mirror
	AlsoTarballOf string `yaml:"also_tarball_of"`

	// apt_closure
	Codename   string      `yaml:"codename"`
	Arch       string      `yaml:"arch"`
	Packages   []string    `yaml:"packages"`
	ExtraRepos []ExtraRepo `yaml:"extra_repos"`
}

// KiwixPinned is the pinned file of a kiwix item with version_policy: pinned.
type KiwixPinned struct {
	File   string `yaml:"file" catalog:"required"`
	SHA256 string `yaml:"sha256"`
}

// URLFile is one file of a url item with a `files` list.
type URLFile struct {
	URL    string `yaml:"url" catalog:"required"`
	SHA256 string `yaml:"sha256"`
	// Name is the path of the file inside the item directory ("release/SHA256SUMS"). It is optional: the
	// default is the last element of the URL's path. It exists for a file whose release name the sync
	// engine reserves in every item directory (SHA256SUMS, MANIFEST.json) or that belongs in a
	// sub-folder (catalog.md section 3, rebuild-kit.md section 3.2.1).
	Name string `yaml:"name"`
}

// ItemPath is the path of f inside its item directory: Name when the catalog gives one, else the last
// element of the URL's path ("" when the URL has none). The caller checks that the result is a usable
// file path (manifest.ValidFilePath).
func (f URLFile) ItemPath() string {
	if f.Name != "" {
		return f.Name
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

// ExtraRepo is a third-party apt repository added to the private apt tree.
type ExtraRepo struct {
	Name       string   `yaml:"name" catalog:"required"`
	URI        string   `yaml:"uri" catalog:"required"`
	Suite      string   `yaml:"suite" catalog:"required"`
	Components []string `yaml:"components" catalog:"required"`
	Key        string   `yaml:"key" catalog:"required"`
}

// PortalLink is a first-aid link of the Survival Portal (kiwix items only).
type PortalLink struct {
	Key       string      `yaml:"key"`
	Title     PortalTitle `yaml:"title" catalog:"required"`
	Article   string      `yaml:"article"`
	ESItem    string      `yaml:"es_item"`
	ESArticle string      `yaml:"es_article"`
	Order     int         `yaml:"order" catalog:"required"`
}

// PortalTitle is the bilingual title of a portal link.
type PortalTitle struct {
	EN string `yaml:"en" catalog:"required"`
	ES string `yaml:"es" catalog:"required"`
}

// Profile is profiles/<id>.yaml (catalog.md section 4).
type Profile struct {
	SchemaVersion int      `yaml:"schema_version" catalog:"required"`
	ID            string   `yaml:"id" catalog:"required"`
	DisplayName   string   `yaml:"display_name" catalog:"required"`
	Match         *Match   `yaml:"match"`
	Router        Router   `yaml:"router" catalog:"required"`
	Storage       Storage  `yaml:"storage" catalog:"required"`
	Include       Include  `yaml:"include" catalog:"required"`
	DefaultModel  string   `yaml:"default_model" catalog:"required"`
	Survival      Survival `yaml:"survival" catalog:"required"`
	// Workloads are the named model sets a person can switch the node to with one action
	// (catalog.md section 4, "Workloads"): name -> label and models. Optional.
	Workloads map[string]Workload `yaml:"workloads"`
}

// Workload is one named set of models (agent.md section 3.8, POST /workloads/{name}/activate).
type Workload struct {
	Label  WorkloadLabel `yaml:"label" catalog:"required"`
	Models []string      `yaml:"models" catalog:"required"`
}

// WorkloadNameRe is the form of a workload name ("coding", "music", "voice-chat").
var WorkloadNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// WorkloadLabel is the bilingual name of a workload.
type WorkloadLabel struct {
	EN string `yaml:"en" catalog:"required"`
	ES string `yaml:"es" catalog:"required"`
}

// Match says which hardware a node profile is for.
type Match struct {
	Backends []string `yaml:"backends"`
	VRAMMiB  *Range   `yaml:"vram_mib"`
	RAMMiB   *Range   `yaml:"ram_mib"`
}

// Range is an inclusive interval; a missing bound is open.
type Range struct {
	Min *int64 `yaml:"min"`
	Max *int64 `yaml:"max"`
}

// Router configures llama-server's router mode for this profile.
type Router struct {
	ModelsMax       int   `yaml:"models_max" catalog:"required"`
	MemoryMarginMiB int   `yaml:"memory_margin_mib"`
	Autoload        *bool `yaml:"autoload"`
}

// Storage is the content-root budget of a profile.
type Storage struct {
	Target       string `yaml:"target" catalog:"required"`
	BudgetBytes  int64  `yaml:"budget_bytes" catalog:"required"`
	MinFreeBytes int64  `yaml:"min_free_bytes" catalog:"required"`
}

// Include lists what the profile keeps.
type Include struct {
	Models  []string `yaml:"models" catalog:"required"`
	Content []string `yaml:"content" catalog:"required"`
}

// Survival is what survival mode needs from this profile.
type Survival struct {
	Models          []string `yaml:"models" catalog:"required"`
	DefaultModel    string   `yaml:"default_model" catalog:"required"`
	RequireServices []string `yaml:"require_services"`
	PortalLanguage  string   `yaml:"portal_language"`
}
