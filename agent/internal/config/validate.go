package config

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

var (
	nodeIDRe   = regexp.MustCompile(`^ai-n[1-9][0-9]{0,3}$`)
	commitRe   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	cudaArchRe = regexp.MustCompile(`^[0-9]{2,3}(;[0-9]{2,3})*$`)
	aliasRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_:]{1,63}$`)
	envKeyRe   = regexp.MustCompile(`^(GGML|LLAMA|CUDA)_[A-Z0-9_]+$`)
)

const maxEnvValueChars = 256

// Load parses node.yaml text and applies every validation rule: Parse followed by Validate, with
// the line of each problem filled in. It is what the store uses. The Config is valid only when
// Problems.HasErrors is false.
func Load(data []byte, h *Hooks) (Config, Problems) {
	root, probs := parseRoot(data)
	if probs.HasErrors() {
		return Defaults(), probs
	}
	cfg, parseProbs := decodeConfig(root, h)
	probs = append(probs, parseProbs...)
	// A field that failed to decode keeps its default; do not pile a second report on it.
	reported := map[string]bool{}
	for _, p := range parseProbs {
		reported[p.Path] = true
	}
	for _, p := range Validate(cfg, h) {
		if !reported[p.Path] {
			probs = append(probs, p)
		}
	}
	fillLines(root, probs)
	return cfg, probs
}

type validator struct{ probs Problems }

func (v *validator) errf(path, format string, args ...any) {
	v.probs = append(v.probs, Problem{Path: path, Message: fmt.Sprintf(format, args...), Severity: SeverityError})
}

func (v *validator) coded(path, code string, sev Severity, format string, args ...any) {
	v.probs = append(v.probs, Problem{Path: path, Message: fmt.Sprintf(format, args...), Code: code, Severity: sev})
}

func oneOf(s string, allowed ...string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

func rng(path string, v *validator, n, lo, hi int) {
	if n < lo || n > hi {
		v.errf(path, "must be a whole number from %d to %d (got %d)", lo, hi, n)
	}
}

// Validate applies the rule table of agent.md 3.4 to cfg. Each violation is one Problem. The
// returned Problems carry no line numbers (Load adds them). Problems of Severity warning do not
// make the config invalid.
func Validate(cfg Config, h *Hooks) Problems {
	v := &validator{}

	if cfg.SchemaVersion != SchemaVersion {
		if cfg.SchemaVersion == 0 {
			v.errf("schema_version", "is required and must be %d", SchemaVersion)
		} else {
			v.errf("schema_version", "must be %d (got %d)", SchemaVersion, cfg.SchemaVersion)
		}
	}

	switch {
	case cfg.Node.ID == "":
		v.errf("node.id", "is required (for example ai-n3)")
	case !nodeIDRe.MatchString(cfg.Node.ID):
		v.errf("node.id", "must look like ai-n3: ai-n, then a number from 1 to 9999 without leading zeros (got %q)", cfg.Node.ID)
	}

	switch {
	case cfg.Profile == "":
		v.errf("profile", "is required (a profile id from the catalog)")
	case h != nil && h.ProfileExists != nil && !h.ProfileExists(cfg.Profile):
		v.coded("profile", CodeProfileMissing, SeverityWarning, "profile %q is not in the catalog", cfg.Profile)
	}

	v.llama(cfg.Llama, h)

	if !oneOf(cfg.Tailscale.ServePattern, "A", "B", "none") {
		v.errf("tailscale.serve_pattern", "must be A, B or none (got %q)", cfg.Tailscale.ServePattern)
	}

	v.cloud(cfg.Cloud)

	if !oneOf(cfg.UI.Language, "auto", "en", "es") {
		v.errf("ui.language", "must be auto, en or es (got %q)", cfg.UI.Language)
	}
	v.ports(cfg.Ports)
	if !oneOf(cfg.Logging.Level, "debug", "info", "warn", "error") {
		v.errf("logging.level", "must be debug, info, warn or error (got %q)", cfg.Logging.Level)
	}

	v.workloads(cfg.Workloads, h)
	v.ownedBlocks(cfg, h)
	return v.probs
}

// workloadNameRe is catalog.WorkloadNameRe (this package does not import the catalog).
var workloadNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// workloads checks workloads{}: a name like the profile's, both labels, at least one model, and, when
// the catalog is known, every model id in it (LAI-CAT-020).
func (v *validator) workloads(ws map[string]Workload, h *Hooks) {
	if len(ws) == 0 {
		return
	}
	var known map[string]bool
	if h != nil && h.CatalogModelIDs != nil {
		if ids := h.CatalogModelIDs(); len(ids) > 0 {
			known = make(map[string]bool, len(ids))
			for _, id := range ids {
				known[id] = true
			}
		}
	}
	names := make([]string, 0, len(ws))
	for n := range ws {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w := ws[n]
		p := "workloads." + n
		if !workloadNameRe.MatchString(n) {
			v.errf(p, "a workload name is lowercase letters, digits and dashes, starting with a letter (got %q)", n)
		}
		if strings.TrimSpace(w.Label.EN) == "" || strings.TrimSpace(w.Label.ES) == "" {
			v.errf(p+".label", "needs both en and es")
		}
		if len(w.Models) == 0 {
			v.errf(p+".models", "must list at least one model")
		}
		for i, id := range w.Models {
			if known != nil && !known[id] {
				// Non-blocking (agent.md section 3.4): the file loads, that workload is left out.
				v.coded(fmt.Sprintf("%s.models[%d]", p, i), CodeWorkloadModel, SeverityWarning, "model %q is not in the catalog; this workload is left out", id)
			}
		}
	}
}

func (v *validator) llama(l LlamaBlock, h *Hooks) {
	if !oneOf(l.Mode, "router", "single") {
		v.errf("llama.mode", "must be router or single (got %q)", l.Mode)
	}
	if l.Backend != "" && !oneOf(l.Backend, "cuda", "vulkan", "cpu") {
		v.errf("llama.backend", "must be cuda, vulkan or cpu (got %q)", l.Backend)
	}
	if l.Commit != "" && !commitRe.MatchString(l.Commit) {
		v.errf("llama.commit", "must be 7 to 40 lowercase hex characters (got %q)", l.Commit)
	}
	if l.CudaArch != nil && !cudaArchRe.MatchString(*l.CudaArch) {
		v.errf("llama.cuda_arch", "must look like 75 or 75;86, or be null (got %q)", *l.CudaArch)
	}
	rng("llama.load_timeout_seconds", v, l.LoadTimeoutSeconds, 30, 3600)

	var catalog map[string]bool
	if h != nil && h.CatalogModelIDs != nil {
		if ids := h.CatalogModelIDs(); len(ids) > 0 {
			catalog = make(map[string]bool, len(ids))
			for _, id := range ids {
				catalog[id] = true
			}
		}
	}
	for _, id := range sortedKeys(l.LegacyAliases) {
		p := "llama.legacy_aliases." + id
		if catalog != nil && !catalog[id] {
			v.errf(p, "the key must be a model id from the catalog")
		}
		for i, alias := range l.LegacyAliases[id] {
			ap := fmt.Sprintf("%s[%d]", p, i)
			switch {
			case !aliasRe.MatchString(alias):
				v.errf(ap, "an alias must be 2 to 64 characters: lowercase letters, digits and . - _ : (got %q)", alias)
			case catalog != nil && catalog[alias]:
				v.errf(ap, "an alias must not equal a catalog model id (got %q)", alias)
			}
		}
	}
	for _, k := range sortedKeys(l.ExtraEnv) {
		p := "llama.extra_env." + k
		switch {
		case strings.HasPrefix(k, "LLAMA_ARG_"):
			v.errf(p, "LLAMA_ARG_* variables are refused: the router sets them")
		case !envKeyRe.MatchString(k):
			v.errf(p, "the name must start with GGML_, LLAMA_ or CUDA_ and use only capital letters, digits and _")
		}
		val := l.ExtraEnv[k]
		if n := len([]rune(val)); n > maxEnvValueChars {
			v.errf(p, "the value is %d characters; the limit is %d", n, maxEnvValueChars)
		} else if strings.IndexFunc(val, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			v.errf(p, "the value must contain only printable characters")
		}
	}
}

func (v *validator) cloud(c CloudBlock) {
	if c.URL != "" { // empty only in the safe defaults, which are never validated
		v.cloudURL(c)
	}
	known := map[string]bool{}
	for _, n := range CloudJobNames() {
		known[n] = true
	}
	for i, name := range c.Allow {
		if !known[name] {
			v.errf(fmt.Sprintf("cloud.allow[%d]", i), "unknown job name %q (see agent.md 4.8.6 for the 14 names)", name)
		}
	}
	rng("cloud.concurrency", v, c.Concurrency, 1, 4)
	rng("cloud.poll.heartbeat_seconds", v, c.Poll.HeartbeatSeconds, 5, 60)
	rng("cloud.poll.claim_seconds", v, c.Poll.ClaimSeconds, 1, 30)
	rng("cloud.poll.state_seconds", v, c.Poll.StateSeconds, 15, 600)
}

// cloudURL checks cloud.url: https://host[:port] without a path, or http://127.0.0.1:<port> with
// dev_insecure: true. Anything that is not https (or the allowed dev form) is LAI-CLD-062.
func (v *validator) cloudURL(c CloudBlock) {
	const path = "cloud.url"
	const notHTTPS = "must start with https:// (http://127.0.0.1:<port> is allowed only with dev_insecure: true)"
	raw := c.URL
	u, err := url.Parse(raw)
	var scheme string
	if err == nil {
		scheme = strings.ToLower(u.Scheme)
	} else if i := strings.Index(raw, "://"); i >= 0 {
		scheme = strings.ToLower(raw[:i])
	}
	switch scheme {
	case "https":
		if err != nil {
			v.errf(path, "is not a valid address: %v", err)
			return
		}
	case "http":
		if err != nil {
			v.coded(path, CodeCloudURLNotHTTPS, SeverityError, notHTTPS)
			return
		}
		port, perr := strconv.Atoi(u.Port())
		if u.Hostname() != "127.0.0.1" || perr != nil || port < 1 || port > 65535 || !c.DevInsecure {
			v.coded(path, CodeCloudURLNotHTTPS, SeverityError, notHTTPS)
			return
		}
	default:
		v.coded(path, CodeCloudURLNotHTTPS, SeverityError, notHTTPS)
		return
	}
	switch {
	case u.Hostname() == "":
		v.errf(path, "needs a host name (for example https://ai.example.test)")
	case u.User != nil:
		v.errf(path, "must not contain a user name or password")
	case u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#"):
		v.errf(path, "must be an address without a path, query or fragment")
	}
}

func (v *validator) ports(p PortsBlock) {
	type port struct {
		name string
		n    int
	}
	all := []port{{"agent", p.Agent}, {"llama", p.Llama}, {"kiwix", p.Kiwix}, {"whisper", p.Whisper}, {"music", p.Music}}
	used := map[int]string{}
	for _, x := range all {
		path := "ports." + x.name
		if x.n < 1024 || x.n > 65535 {
			v.errf(path, "must be a whole number from 1024 to 65535 (got %d)", x.n)
			continue
		}
		if other, dup := used[x.n]; dup {
			v.errf(path, "must differ from ports.%s (both are %d)", other, x.n)
			continue
		}
		used[x.n] = x.name
	}
}

// ownedBlocks runs the validators the owning packages registered, in block order.
func (v *validator) ownedBlocks(cfg Config, h *Hooks) {
	if h == nil {
		return
	}
	for _, name := range OwnedBlockNames {
		bv, ok := h.Blocks[name]
		raw, present := cfg.Blocks[name]
		if !ok || bv == nil || !present {
			continue
		}
		for _, p := range bv.ValidateBlock(raw) {
			if p.Severity == "" {
				p.Severity = SeverityError
			}
			if p.Path == "" {
				p.Path = name
			} else {
				p.Path = name + "." + p.Path
			}
			v.probs = append(v.probs, p)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fillLines sets Problem.Line for problems that only know their path, by looking the path up in
// the node tree (falling back to the nearest parent that exists).
func fillLines(root *yaml.Node, probs Problems) {
	if root == nil {
		return
	}
	for i := range probs {
		if probs[i].Line != 0 || probs[i].Path == "" {
			continue
		}
		probs[i].Line = lineOf(root, probs[i].Path)
	}
}

var indexRe = regexp.MustCompile(`^(.*)\[(\d+)\]$`)

func lineOf(root *yaml.Node, path string) int {
	n := root
	line := 0
	for _, seg := range strings.Split(path, ".") {
		idx := -1
		if m := indexRe.FindStringSubmatch(seg); m != nil {
			seg = m[1]
			idx, _ = strconv.Atoi(m[2])
		}
		n = resolveAlias(n)
		if n == nil || n.Kind != yaml.MappingNode {
			return line
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == seg {
				line = n.Content[i].Line
				next = resolveAlias(n.Content[i+1])
				break
			}
		}
		if next == nil {
			return line
		}
		n = next
		if idx >= 0 {
			if n.Kind != yaml.SequenceNode || idx >= len(n.Content) {
				return line
			}
			n = n.Content[idx]
			line = n.Line
		}
	}
	return line
}
