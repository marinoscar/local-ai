// Package config is the agent's config store (agent.md sections 3.3 and 3.4): the typed
// /etc/localai/node.yaml schema, its validation, the atomic file writer shared by every
// agent-owned file, the comment-preserving Store, and hot reload.
//
// The rule that shapes everything here: a missing or broken node.yaml never stops the agent.
// Open always returns a usable Store (safe defaults) and reports why through Status.
package config

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only node.yaml schema_version this agent understands.
const SchemaVersion = 1

// OwnedBlockNames are the top-level blocks whose schema and validation belong to other specs
// (ark-sync.md 3.3, companion-services.md 3.1, survival-portal.md 3.4). They are parsed and
// kept as RawBlock; their packages register a BlockValidator through Hooks.
var OwnedBlockNames = []string{"sync", "kiwix", "whisper", "piper", "maps", "portal"}

// Severity says whether a Problem blocks loading the file.
type Severity string

const (
	// SeverityError: the file is invalid; the last valid config stays in use (or safe defaults at start).
	SeverityError Severity = "error"
	// SeverityWarning: the file loads anyway (unknown top-level key, profile not in the catalog).
	SeverityWarning Severity = "warning"
)

// Problem is one entry of details.problems[] in a LAI-AGT-002 report. Code is set only when a more
// specific doctor code exists (cloud.url -> CodeCloudURLNotHTTPS, profile -> CodeProfileMissing).
// Severity is about loading the file; the doctor severity of a code comes from the code table.
type Problem struct {
	Path     string   `json:"path"`
	Message  string   `json:"message"`
	Code     string   `json:"code,omitempty"`
	Severity Severity `json:"severity"`
	Line     int      `json:"line,omitempty"` // 1-based line in node.yaml, when known
}

// Problems is a validation report.
type Problems []Problem

// HasErrors reports whether any problem blocks loading.
func (p Problems) HasErrors() bool {
	for _, x := range p {
		if x.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns only the blocking problems.
func (p Problems) Errors() Problems { return p.filter(SeverityError) }

// Warnings returns only the non-blocking problems.
func (p Problems) Warnings() Problems { return p.filter(SeverityWarning) }

func (p Problems) filter(s Severity) Problems {
	var out Problems
	for _, x := range p {
		if x.Severity == s {
			out = append(out, x)
		}
	}
	return out
}

// ValidationError is returned by Store.Update when the edited file would be invalid. Nothing was written.
type ValidationError struct{ Problems Problems }

func (e *ValidationError) Error() string {
	errs := e.Problems.Errors()
	if len(errs) == 0 {
		return "node.yaml is invalid"
	}
	parts := make([]string, 0, len(errs))
	for _, p := range errs {
		if p.Path == "" {
			parts = append(parts, p.Message)
		} else {
			parts = append(parts, p.Path+": "+p.Message)
		}
	}
	return "node.yaml would be invalid: " + strings.Join(parts, "; ")
}

// Hooks are the seams to subsystems that load later than this package. Every field is optional.
type Hooks struct {
	// ProfileExists answers whether the loaded catalog has the profile. nil: not checked.
	ProfileExists func(id string) bool
	// CatalogModelIDs lists the catalog's model ids. nil or empty: the catalog-dependent
	// llama.legacy_aliases checks are skipped (an empty catalog must not invalidate the file).
	CatalogModelIDs func() []string
	// DetectBackend fills llama.backend when absent (nvidia -> cuda, amd -> vulkan, else cpu).
	DetectBackend func() string
	// BuildCommit fills llama.commit when absent (from current/BUILD.json).
	BuildCommit func() string
	// Blocks holds the validators the owning packages register, keyed by block name.
	Blocks map[string]BlockValidator
}

// BlockValidator is implemented by the packages that own a node.yaml block (sync, kiwix, whisper,
// piper, maps, portal). ValidateBlock sees the block exactly as written; the Path of each returned
// problem is relative to the block, and the store prefixes the block name.
type BlockValidator interface {
	ValidateBlock(raw RawBlock) Problems
}

// RawBlock is one owned block, kept as YAML text.
type RawBlock []byte

// Decode reads the block into v (unknown keys are ignored).
func (b RawBlock) Decode(v any) error { return yaml.Unmarshal(b, v) }

// DecodeStrict reads the block into v and rejects unknown keys.
func (b RawBlock) DecodeStrict(v any) error {
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	return d.Decode(v)
}

// Config is the typed node.yaml (agent.md section 3.4). Absent fields hold their defaults.
type Config struct {
	SchemaVersion    int              `yaml:"schema_version"`
	Node             NodeBlock        `yaml:"node"`
	Profile          string           `yaml:"profile"`
	ProfileOverrides ProfileOverrides `yaml:"profile_overrides"`
	Catalog          CatalogBlock     `yaml:"catalog"`
	Llama            LlamaBlock       `yaml:"llama"`
	Tailscale        TailscaleBlock   `yaml:"tailscale"`
	Cloud            CloudBlock       `yaml:"cloud"`
	Update           UpdateBlock      `yaml:"update"`
	UI               UIBlock          `yaml:"ui"`
	Ports            PortsBlock       `yaml:"ports"`
	Logging          LoggingBlock     `yaml:"logging"`

	// Blocks holds the owned blocks that are present in the file, by name.
	Blocks map[string]RawBlock `yaml:"-"`
	// SafeDefaults is true when the agent runs on the built-in safe defaults because node.yaml
	// was missing or invalid at start.
	SafeDefaults bool `yaml:"-"`
}

// NodeBlock is node{}.
type NodeBlock struct {
	ID          string  `yaml:"id"`
	DisplayName string  `yaml:"display_name"`
	PublicHost  *string `yaml:"public_host"`
}

// ProfileOverrides is profile_overrides{} (catalog.md section 4, "Node overrides").
type ProfileOverrides struct {
	AddModels     []string `yaml:"add_models"`
	RemoveModels  []string `yaml:"remove_models"`
	AddContent    []string `yaml:"add_content"`
	RemoveContent []string `yaml:"remove_content"`
	BudgetBytes   *int64   `yaml:"budget_bytes"`
}

// CatalogBlock is catalog{}.
type CatalogBlock struct {
	Channel string `yaml:"channel"`
	BaseURL string `yaml:"base_url"`
}

// LlamaBlock is llama{}.
type LlamaBlock struct {
	Mode               string              `yaml:"mode"`
	Build              string              `yaml:"build"`
	Commit             string              `yaml:"commit"`
	Backend            string              `yaml:"backend"`
	CudaArch           *string             `yaml:"cuda_arch"`
	LoadTimeoutSeconds int                 `yaml:"load_timeout_seconds"`
	LegacyAliases      map[string][]string `yaml:"legacy_aliases"`
	ExtraEnv           map[string]string   `yaml:"extra_env"`
}

// TailscaleBlock is tailscale{}.
type TailscaleBlock struct {
	Manage       bool   `yaml:"manage"`
	ServePattern string `yaml:"serve_pattern"`
}

// CloudBlock is cloud{}. An empty URL means "no cloud" and only occurs in the safe defaults.
type CloudBlock struct {
	URL         string    `yaml:"url"`
	DevInsecure bool      `yaml:"dev_insecure"`
	Allow       []string  `yaml:"allow"`
	Concurrency int       `yaml:"concurrency"`
	Poll        PollBlock `yaml:"poll"`
}

// PollBlock is cloud.poll{}.
type PollBlock struct {
	HeartbeatSeconds int `yaml:"heartbeat_seconds"`
	ClaimSeconds     int `yaml:"claim_seconds"`
	StateSeconds     int `yaml:"state_seconds"`
}

// UpdateBlock is update{}.
type UpdateBlock struct {
	Repo           string `yaml:"repo"`
	Channel        string `yaml:"channel"`
	AllowDowngrade bool   `yaml:"allow_downgrade"`
}

// UIBlock is ui{}.
type UIBlock struct {
	Language string `yaml:"language"`
}

// PortsBlock is ports{}.
type PortsBlock struct {
	Agent   int `yaml:"agent"`
	Llama   int `yaml:"llama"`
	Kiwix   int `yaml:"kiwix"`
	Whisper int `yaml:"whisper"`
}

// LoggingBlock is logging{}.
type LoggingBlock struct {
	Level string `yaml:"level"`
}

// CloudEnabled reports whether a cloud link is configured (false only in the safe defaults).
func (c Config) CloudEnabled() bool { return c.Cloud.URL != "" }

// Clone returns a deep copy, so a snapshot handed to a caller can never alias the store's.
func (c Config) Clone() Config {
	out := c
	out.Node.PublicHost = cloneStrPtr(c.Node.PublicHost)
	out.ProfileOverrides.AddModels = cloneStrs(c.ProfileOverrides.AddModels)
	out.ProfileOverrides.RemoveModels = cloneStrs(c.ProfileOverrides.RemoveModels)
	out.ProfileOverrides.AddContent = cloneStrs(c.ProfileOverrides.AddContent)
	out.ProfileOverrides.RemoveContent = cloneStrs(c.ProfileOverrides.RemoveContent)
	if c.ProfileOverrides.BudgetBytes != nil {
		v := *c.ProfileOverrides.BudgetBytes
		out.ProfileOverrides.BudgetBytes = &v
	}
	out.Llama.CudaArch = cloneStrPtr(c.Llama.CudaArch)
	if c.Llama.LegacyAliases != nil {
		out.Llama.LegacyAliases = make(map[string][]string, len(c.Llama.LegacyAliases))
		for k, v := range c.Llama.LegacyAliases {
			out.Llama.LegacyAliases[k] = cloneStrs(v)
		}
	}
	if c.Llama.ExtraEnv != nil {
		out.Llama.ExtraEnv = make(map[string]string, len(c.Llama.ExtraEnv))
		for k, v := range c.Llama.ExtraEnv {
			out.Llama.ExtraEnv[k] = v
		}
	}
	out.Cloud.Allow = cloneStrs(c.Cloud.Allow)
	if c.Blocks != nil {
		out.Blocks = make(map[string]RawBlock, len(c.Blocks))
		for k, v := range c.Blocks {
			out.Blocks[k] = append(RawBlock(nil), v...)
		}
	}
	return out
}

// cloneStrs copies a slice and keeps nil nil, so a clone compares equal to its original.
func cloneStrs(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string{}, s...)
}

func cloneStrPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Parse reads node.yaml text into a Config without applying the validation rules: it reports YAML
// syntax errors, values of the wrong type, repeated keys and unknown keys (a warning at the top
// level, an error inside a block). Absent fields get their defaults. The Config is always usable
// as a best effort; check Problems.HasErrors before trusting it. Most callers want Load.
func Parse(data []byte, h *Hooks) (Config, Problems) {
	root, probs := parseRoot(data)
	if probs.HasErrors() {
		return Defaults(), probs
	}
	cfg, more := decodeConfig(root, h)
	return cfg, append(probs, more...)
}

// parseRoot returns the top-level mapping node of data (nil for an empty file).
func parseRoot(data []byte) (*yaml.Node, Problems) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{{Message: "not valid YAML: " + strings.TrimPrefix(err.Error(), "yaml: "), Severity: SeverityError}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil
	}
	root := resolveAlias(doc.Content[0])
	if isNull(root) {
		return nil, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, Problems{{Message: "the top level must be a map of settings", Severity: SeverityError, Line: root.Line}}
	}
	return root, nil
}

func decodeConfig(root *yaml.Node, h *Hooks) (Config, Problems) {
	cfg := Defaults()
	d := &decoder{blocks: map[string]RawBlock{}}
	if root != nil {
		d.decodeStruct(root, reflect.ValueOf(&cfg).Elem(), "", true)
	}
	if len(d.blocks) > 0 {
		cfg.Blocks = d.blocks
	}
	if cfg.Node.DisplayName == "" {
		cfg.Node.DisplayName = cfg.Node.ID
	}
	cfg.Cloud.URL = strings.TrimSuffix(cfg.Cloud.URL, "/")
	if cfg.Llama.Backend == "" && h != nil && h.DetectBackend != nil {
		cfg.Llama.Backend = h.DetectBackend()
	}
	if cfg.Llama.Commit == "" && h != nil && h.BuildCommit != nil {
		cfg.Llama.Commit = h.BuildCommit()
	}
	return cfg, d.probs
}

// decoder walks the node tree into the Config structs. It does its own walk (instead of
// yaml.Node.Decode) so every problem carries a dotted path and a line, and so unknown keys can be
// a warning at the top level and an error inside a block.
type decoder struct {
	probs  Problems
	blocks map[string]RawBlock
}

func (d *decoder) add(sev Severity, path, msg string, line int) {
	d.probs = append(d.probs, Problem{Path: path, Message: msg, Severity: sev, Line: line})
}

func (d *decoder) decodeStruct(n *yaml.Node, v reflect.Value, path string, top bool) {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		kn, vn := n.Content[i], resolveAlias(n.Content[i+1])
		if kn.Kind != yaml.ScalarNode {
			d.add(SeverityError, path, "setting names must be plain text", kn.Line)
			continue
		}
		key := kn.Value
		p := joinPath(path, key)
		if seen[key] {
			d.add(SeverityError, p, "this setting appears twice", kn.Line)
			continue
		}
		seen[key] = true
		if top && isOwnedBlock(key) {
			d.keepBlock(key, vn)
			continue
		}
		fi := fieldIndex(v.Type(), key)
		if fi < 0 {
			if top {
				d.add(SeverityWarning, p, "unknown setting, ignored (a newer agent may use it)", kn.Line)
			} else {
				d.add(SeverityError, p, "unknown setting", kn.Line)
			}
			continue
		}
		d.decodeValue(vn, v.Field(fi), p)
	}
}

func (d *decoder) keepBlock(name string, n *yaml.Node) {
	if isNull(n) {
		return
	}
	if n.Kind != yaml.MappingNode {
		d.add(SeverityError, name, "must be a group of settings", n.Line)
		return
	}
	raw, err := yaml.Marshal(n)
	if err != nil {
		d.add(SeverityError, name, "cannot be read: "+err.Error(), n.Line)
		return
	}
	d.blocks[name] = raw
}

func (d *decoder) decodeValue(n *yaml.Node, fv reflect.Value, path string) {
	null := isNull(n)
	if fv.Kind() == reflect.Struct {
		switch {
		case null:
		case n.Kind != yaml.MappingNode:
			d.add(SeverityError, path, "must be a group of settings", n.Line)
		default:
			d.decodeStruct(n, fv, path, false)
		}
		return
	}
	if null {
		// An explicit null clears an optional (pointer) field and means "use the default" otherwise.
		if fv.Kind() == reflect.Ptr {
			fv.Set(reflect.Zero(fv.Type()))
		}
		return
	}
	tmp := reflect.New(fv.Type())
	if err := n.Decode(tmp.Interface()); err != nil {
		d.add(SeverityError, path, "must be "+typeWord(fv.Type()), n.Line)
		return
	}
	fv.Set(tmp.Elem())
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func isOwnedBlock(name string) bool {
	for _, b := range OwnedBlockNames {
		if b == name {
			return true
		}
	}
	return false
}

func fieldIndex(t reflect.Type, key string) int {
	for i := 0; i < t.NumField(); i++ {
		if name := yamlName(t.Field(i)); name != "" && name != "-" && name == key {
			return i
		}
	}
	return -1
}

func yamlName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if i := strings.Index(tag, ","); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// typeWord names a field type in plain language for "must be ..." messages.
func typeWord(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Ptr:
		return typeWord(t.Elem())
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "a whole number"
	case reflect.String:
		return "text"
	case reflect.Slice:
		return "a list of " + typeWord(t.Elem())
	case reflect.Map:
		return fmt.Sprintf("a map of %s to %s", typeWord(t.Key()), typeWord(t.Elem()))
	default:
		return "a " + t.Kind().String()
	}
}
