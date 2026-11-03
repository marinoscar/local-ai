// Package kit is the rebuild kit as data (docs/specs/rebuild-kit.md): where each kit item of the
// catalog lives under <root>/kit/ (Layout), the pins.json that tells a drive which sources to keep
// for every node of the lab (Pins and its merge), and the generated kit/MANIFEST.json index and
// kit/README.txt (BuildIndex, WriteIndex).
//
// Nothing here downloads, verifies a content hash or builds: the resolvers (ark-sync.md section 4.9)
// fetch, kit verification (KIT-03) judges, the build engine (KIT-04) builds. This package only
// decides names and paths, merges one small JSON file and writes two generated ones, all through
// config.FS so tests inject faults, and all deterministic: the same input gives the same bytes.
package kit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/jsonord"
)

// Pins file facts (rebuild-kit.md section 3.3).
const (
	// PinsFileName is the file name; it sits in <root>/kit/catalog/ next to the catalog copy.
	PinsFileName = "pins.json"
	// PinsSchemaVersion is the only schema_version this code reads and writes.
	PinsSchemaVersion = 1
	// PinsMaxAge is how long an entry that no node reports any more is kept: 180 days.
	PinsMaxAge = 180 * 24 * time.Hour

	maxPinsBytes = 4 << 20
	maxPinNodes  = 256
)

// CodePinsDamaged is the doctor code of a pins.json that cannot be read or is from a newer schema
// (LAI-KIT-045). The file is never rewritten in that state.
const CodePinsDamaged = doctor.KIT045

// Errors of ParsePins and ReadPins. All of them are LAI-KIT-045 (CodePinsDamaged). Test with errors.Is.
var (
	// ErrPinsCorrupt: not JSON, a field of the wrong type, a malformed commit, tag, codename, arch,
	// branch or node id, or a duplicate entry.
	ErrPinsCorrupt = errors.New("kit: pins.json is damaged")
	// ErrPinsSchema: schema_version is not 1. A newer file is left alone: rewriting it would drop
	// what this code does not know.
	ErrPinsSchema = errors.New("kit: unsupported pins.json schema_version")
)

var (
	commitRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	tagRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,63}$`)
	codenameRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)
	branchRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,31}$`)
	nodeIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._\-]{0,62}$`)
)

// Architectures the kit knows (the catalog's `arch` values).
var arches = []string{"amd64", "arm64"}

// CommitPin says that some nodes build one commit of llama.cpp or whisper.cpp.
type CommitPin struct {
	// Commit is the full 40-hex commit.
	Commit string
	// Nodes are the node ids that use it, sorted, without duplicates.
	Nodes []string
	// ReportedAt is the last time any node reported this entry. The zero value means "not stated":
	// a merge then takes the updated_at of the file the entry came from.
	ReportedAt time.Time
}

// AgentPin says that some nodes run one agent release.
type AgentPin struct {
	// Tag is the release tag, for example node-v0.4.0.
	Tag        string
	Nodes      []string
	ReportedAt time.Time
}

// CodenamePin says that some nodes run one Ubuntu codename and architecture.
type CodenamePin struct {
	Codename   string
	Arch       string
	Nodes      []string
	ReportedAt time.Time
}

// NvidiaPin says that some nodes use one NVIDIA driver branch on one codename and architecture.
type NvidiaPin struct {
	// Branch is the driver branch with its flavour, for example 595-open.
	Branch     string
	Codename   string
	Arch       string
	Nodes      []string
	ReportedAt time.Time
}

// Pins is kit/catalog/pins.json: what every node of the lab uses now, so a drive keeps the sources
// for all of them (rebuild-kit.md section 3.3). The zero value is an empty, valid-to-merge file.
type Pins struct {
	// UpdatedAt is the last time any node merged into the file.
	UpdatedAt      time.Time
	LlamaCpp       []CommitPin
	WhisperCpp     []CommitPin
	Agent          []AgentPin
	Codenames      []CodenamePin
	NvidiaBranches []NvidiaPin

	// extra keeps top-level keys this version does not know, so a newer writer's file survives a
	// merge by an older agent within schema 1.
	extra map[string]json.RawMessage
}

// NodePins is what one node reports about itself: the inputs of Report. Empty fields are left out
// (a node without whisper reports no whisper commit; a CPU node reports no NVIDIA branch).
type NodePins struct {
	NodeID         string
	LlamaCommit    string
	WhisperCommit  string
	AgentTag       string
	Codename       string
	Arch           string
	NvidiaBranch   string
	NvidiaCodename string // defaults to Codename
}

// Report builds the Pins a node writes into its own root: its entries, stamped now. Merge folds it
// into whatever the root already holds. An invalid field is an error naming it, so a typo never
// reaches the file.
func Report(n NodePins, now time.Time) (Pins, error) {
	if !nodeIDRe.MatchString(n.NodeID) {
		return Pins{}, fmt.Errorf("%w: node id %q is not a node id", ErrPinsCorrupt, trunc(n.NodeID))
	}
	now = stamp(now)
	nodes := []string{n.NodeID}
	p := Pins{UpdatedAt: now}
	if n.LlamaCommit != "" {
		p.LlamaCpp = []CommitPin{{Commit: n.LlamaCommit, Nodes: nodes, ReportedAt: now}}
	}
	if n.WhisperCommit != "" {
		p.WhisperCpp = []CommitPin{{Commit: n.WhisperCommit, Nodes: nodes, ReportedAt: now}}
	}
	if n.AgentTag != "" {
		p.Agent = []AgentPin{{Tag: n.AgentTag, Nodes: nodes, ReportedAt: now}}
	}
	if n.Codename != "" || n.Arch != "" {
		p.Codenames = []CodenamePin{{Codename: n.Codename, Arch: n.Arch, Nodes: nodes, ReportedAt: now}}
	}
	if n.NvidiaBranch != "" {
		cn := n.NvidiaCodename
		if cn == "" {
			cn = n.Codename
		}
		p.NvidiaBranches = []NvidiaPin{{Branch: n.NvidiaBranch, Codename: cn, Arch: n.Arch, Nodes: nodes, ReportedAt: now}}
	}
	if err := p.Validate(); err != nil {
		return Pins{}, err
	}
	return p, nil
}

// Validate checks every entry: formats, at least one node per entry, no duplicate entry. It is what
// ParsePins and Encode run, so a file that parses is a file that merges.
func (p Pins) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrPinsCorrupt, fmt.Sprintf(format, args...))
	}
	nodes := func(where string, ns []string) error {
		if len(ns) == 0 {
			return bad("%s lists no node", where)
		}
		if len(ns) > maxPinNodes {
			return bad("%s lists %d nodes (at most %d)", where, len(ns), maxPinNodes)
		}
		seen := map[string]bool{}
		for _, n := range ns {
			if !nodeIDRe.MatchString(n) {
				return bad("%s: %q is not a node id", where, trunc(n))
			}
			if seen[n] {
				return bad("%s lists node %q twice", where, n)
			}
			seen[n] = true
		}
		return nil
	}
	commits := func(field string, list []CommitPin) error {
		seen := map[string]bool{}
		for i, c := range list {
			where := fmt.Sprintf("%s[%d]", field, i)
			if !commitRe.MatchString(c.Commit) {
				return bad("%s: commit %q is not 40 lowercase hex digits", where, trunc(c.Commit))
			}
			if seen[c.Commit] {
				return bad("%s: commit %s appears twice", where, c.Commit[:12])
			}
			seen[c.Commit] = true
			if err := nodes(where, c.Nodes); err != nil {
				return err
			}
		}
		return nil
	}
	if err := commits("llama_cpp", p.LlamaCpp); err != nil {
		return err
	}
	if err := commits("whisper_cpp", p.WhisperCpp); err != nil {
		return err
	}
	seenTag := map[string]bool{}
	for i, a := range p.Agent {
		where := fmt.Sprintf("agent[%d]", i)
		if !tagRe.MatchString(a.Tag) {
			return bad("%s: tag %q is not a release tag", where, trunc(a.Tag))
		}
		if seenTag[a.Tag] {
			return bad("%s: tag %s appears twice", where, a.Tag)
		}
		seenTag[a.Tag] = true
		if err := nodes(where, a.Nodes); err != nil {
			return err
		}
	}
	seenCN := map[string]bool{}
	for i, c := range p.Codenames {
		where := fmt.Sprintf("codenames[%d]", i)
		if err := checkCodenameArch(where, c.Codename, c.Arch); err != nil {
			return err
		}
		k := c.Codename + "/" + c.Arch
		if seenCN[k] {
			return bad("%s: %s appears twice", where, k)
		}
		seenCN[k] = true
		if err := nodes(where, c.Nodes); err != nil {
			return err
		}
	}
	seenNV := map[string]bool{}
	for i, n := range p.NvidiaBranches {
		where := fmt.Sprintf("nvidia_branches[%d]", i)
		if !branchRe.MatchString(n.Branch) {
			return bad("%s: branch %q is not a driver branch", where, trunc(n.Branch))
		}
		if err := checkCodenameArch(where, n.Codename, n.Arch); err != nil {
			return err
		}
		k := n.Branch + "/" + n.Codename + "/" + n.Arch
		if seenNV[k] {
			return bad("%s: %s appears twice", where, k)
		}
		seenNV[k] = true
		if err := nodes(where, n.Nodes); err != nil {
			return err
		}
	}
	return nil
}

func checkCodenameArch(where, codename, arch string) error {
	if !codenameRe.MatchString(codename) {
		return fmt.Errorf("%w: %s: codename %q is not an Ubuntu codename", ErrPinsCorrupt, where, trunc(codename))
	}
	for _, a := range arches {
		if arch == a {
			return nil
		}
	}
	return fmt.Errorf("%w: %s: arch %q is not amd64 or arm64", ErrPinsCorrupt, where, trunc(arch))
}

func trunc(s string) string {
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}

// stamp is a time as the file stores it: UTC, to the second.
func stamp(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// ---------------------------------------------------------------------------------------------
// Merge

// Merge folds b into a and returns the result: the union of every entry (a node list is the union
// of the nodes of the two entries, the report time the later one), minus the entries that no node
// has reported for PinsMaxAge as of now. Nothing a node reported is removed because another node
// stopped using it; an entry only leaves by growing old (rebuild-kit.md section 3.3).
//
// The merge is commutative and idempotent for a fixed now: Merge(a, b) equals Merge(b, a), and
// merging the result with either input again changes nothing. An entry that carries no report time
// takes the updated_at of its own file (so an entry written by the cloud ages from that moment); the
// result always states every report time. updated_at is the later of the two.
func Merge(a, b Pins, now time.Time) Pins {
	now = stamp(now)
	out := Pins{UpdatedAt: laterOf(a.UpdatedAt, b.UpdatedAt)}
	// Unknown top-level keys: for a key both sides have, the smaller value (byte order) wins, so the
	// result does not depend on the order of the arguments.
	out.extra = mergeExtra(a.extra, b.extra)

	out.LlamaCpp = mergeList(a.LlamaCpp, b.LlamaCpp, a.UpdatedAt, b.UpdatedAt, now,
		func(e CommitPin) string { return e.Commit },
		func(e *CommitPin) (*[]string, *time.Time) { return &e.Nodes, &e.ReportedAt },
		func(x, y CommitPin) bool { return x.Commit < y.Commit })
	out.WhisperCpp = mergeList(a.WhisperCpp, b.WhisperCpp, a.UpdatedAt, b.UpdatedAt, now,
		func(e CommitPin) string { return e.Commit },
		func(e *CommitPin) (*[]string, *time.Time) { return &e.Nodes, &e.ReportedAt },
		func(x, y CommitPin) bool { return x.Commit < y.Commit })
	out.Agent = mergeList(a.Agent, b.Agent, a.UpdatedAt, b.UpdatedAt, now,
		func(e AgentPin) string { return e.Tag },
		func(e *AgentPin) (*[]string, *time.Time) { return &e.Nodes, &e.ReportedAt },
		func(x, y AgentPin) bool { return x.Tag < y.Tag })
	out.Codenames = mergeList(a.Codenames, b.Codenames, a.UpdatedAt, b.UpdatedAt, now,
		func(e CodenamePin) string { return e.Codename + "/" + e.Arch },
		func(e *CodenamePin) (*[]string, *time.Time) { return &e.Nodes, &e.ReportedAt },
		func(x, y CodenamePin) bool { return lessCodename(x.Codename, x.Arch, y.Codename, y.Arch) })
	out.NvidiaBranches = mergeList(a.NvidiaBranches, b.NvidiaBranches, a.UpdatedAt, b.UpdatedAt, now,
		func(e NvidiaPin) string { return e.Branch + "/" + e.Codename + "/" + e.Arch },
		func(e *NvidiaPin) (*[]string, *time.Time) { return &e.Nodes, &e.ReportedAt },
		lessNvidia)
	return out
}

func lessCodename(c1, a1, c2, a2 string) bool { return c1 < c2 || c1 == c2 && a1 < a2 }

func lessNvidia(x, y NvidiaPin) bool {
	if x.Branch != y.Branch {
		return x.Branch < y.Branch
	}
	return lessCodename(x.Codename, x.Arch, y.Codename, y.Arch)
}

// mergeList merges two entry lists of one kind: entries with the same key become one (nodes
// united, later report time), entries whose report time is older than PinsMaxAge are dropped, and
// the rest are sorted by less. acc gives access to an entry's node list and report time.
func mergeList[T any](a, b []T, aFile, bFile, now time.Time, key func(T) string,
	acc func(*T) (*[]string, *time.Time), less func(x, y T) bool) []T {
	m := map[string]*T{}
	for _, side := range []struct {
		list []T
		file time.Time
	}{{a, aFile}, {b, bFile}} {
		for _, e := range side.list {
			e := e
			ns, at := acc(&e)
			*at = reportTime(*at, side.file)
			*ns = unionNodes(nil, *ns)
			if cur, ok := m[key(e)]; ok {
				curNodes, curAt := acc(cur)
				*curNodes = unionNodes(*curNodes, *ns)
				*curAt = laterOf(*curAt, *at)
				continue
			}
			m[key(e)] = &e
		}
	}
	var out []T
	for _, e := range m {
		if _, at := acc(e); !stale(*at, now) {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// reportTime is the report time of an entry: its own, else the updated_at of its file.
func reportTime(own, file time.Time) time.Time {
	if !own.IsZero() {
		return stamp(own)
	}
	return stamp(file)
}

// stale: an entry whose last report is older than PinsMaxAge. An entry whose time is unknown (zero:
// neither the entry nor its file said when) never goes stale on its own: dropping a pin because a
// file was undated would delete a source tarball the lab still needs.
func stale(at, now time.Time) bool {
	if at.IsZero() {
		return false
	}
	return now.Sub(at) > PinsMaxAge
}

func laterOf(a, b time.Time) time.Time {
	a, b = stamp(a), stamp(b)
	if b.After(a) {
		return b
	}
	return a
}

func unionNodes(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, n := range list {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func mergeExtra(a, b map[string]json.RawMessage) map[string]json.RawMessage {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if cur, ok := out[k]; !ok || bytes.Compare(v, cur) < 0 {
			out[k] = v
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// Queries (what KIT-03, the source resolvers and the build engine ask)

// LlamaCommits returns the llama.cpp commits that some node uses, sorted; with node set, only that
// node's.
func (p Pins) LlamaCommits(node string) []string { return commitsOf(p.LlamaCpp, node) }

// WhisperCommits returns the whisper.cpp commits in use, sorted; with node set, only that node's.
func (p Pins) WhisperCommits(node string) []string { return commitsOf(p.WhisperCpp, node) }

func commitsOf(list []CommitPin, node string) []string {
	var out []string
	for _, e := range list {
		if node == "" || hasNode(e.Nodes, node) {
			out = append(out, e.Commit)
		}
	}
	sort.Strings(out)
	return out
}

// AgentTags returns the agent release tags in use, sorted; with node set, only that node's.
func (p Pins) AgentTags(node string) []string {
	var out []string
	for _, e := range p.Agent {
		if node == "" || hasNode(e.Nodes, node) {
			out = append(out, e.Tag)
		}
	}
	sort.Strings(out)
	return out
}

// NodesOf returns every node id that appears anywhere in the pins, sorted.
func (p Pins) NodesOf() []string {
	var all []string
	add := func(ns []string) { all = unionNodes(all, ns) }
	for _, e := range p.LlamaCpp {
		add(e.Nodes)
	}
	for _, e := range p.WhisperCpp {
		add(e.Nodes)
	}
	for _, e := range p.Agent {
		add(e.Nodes)
	}
	for _, e := range p.Codenames {
		add(e.Nodes)
	}
	for _, e := range p.NvidiaBranches {
		add(e.Nodes)
	}
	return all
}

func hasNode(ns []string, n string) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------------
// Encoding

// pinsKeys are the known top-level keys, in output order.
const (
	keySchema   = "schema_version"
	keyUpdated  = "updated_at"
	keyLlama    = "llama_cpp"
	keyWhisper  = "whisper_cpp"
	keyAgent    = "agent"
	keyCodename = "codenames"
	keyNvidia   = "nvidia_branches"
)

type wireCommit struct {
	Commit     string         `json:"commit"`
	Nodes      []string       `json:"nodes"`
	ReportedAt *jsonord.Stamp `json:"reported_at,omitempty"`
}

type wireAgent struct {
	Tag        string         `json:"tag"`
	Nodes      []string       `json:"nodes"`
	ReportedAt *jsonord.Stamp `json:"reported_at,omitempty"`
}

type wireCodename struct {
	Codename   string         `json:"codename"`
	Arch       string         `json:"arch"`
	Nodes      []string       `json:"nodes"`
	ReportedAt *jsonord.Stamp `json:"reported_at,omitempty"`
}

type wireNvidia struct {
	Branch     string         `json:"branch"`
	Codename   string         `json:"codename"`
	Arch       string         `json:"arch"`
	Nodes      []string       `json:"nodes"`
	ReportedAt *jsonord.Stamp `json:"reported_at,omitempty"`
}

func optStamp(t time.Time) *jsonord.Stamp {
	if t.IsZero() {
		return nil
	}
	s := jsonord.Stamp(t)
	return &s
}

func fromStamp(s *jsonord.Stamp) time.Time {
	if s == nil {
		return time.Time{}
	}
	return stamp(time.Time(*s))
}

// Encode validates p and returns the bytes of pins.json: the keys in the order of the spec, every
// list sorted, nodes sorted, indented by two spaces, trailing newline. The same value always gives
// the same bytes. Entries are written exactly as held; call Merge to normalise a hand-built value.
func (p Pins) Encode() ([]byte, error) {
	p = p.sorted()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	llama := make([]wireCommit, 0, len(p.LlamaCpp))
	for _, e := range p.LlamaCpp {
		llama = append(llama, wireCommit{e.Commit, e.Nodes, optStamp(e.ReportedAt)})
	}
	whisper := make([]wireCommit, 0, len(p.WhisperCpp))
	for _, e := range p.WhisperCpp {
		whisper = append(whisper, wireCommit{e.Commit, e.Nodes, optStamp(e.ReportedAt)})
	}
	agent := make([]wireAgent, 0, len(p.Agent))
	for _, e := range p.Agent {
		agent = append(agent, wireAgent{e.Tag, e.Nodes, optStamp(e.ReportedAt)})
	}
	cns := make([]wireCodename, 0, len(p.Codenames))
	for _, e := range p.Codenames {
		cns = append(cns, wireCodename{e.Codename, e.Arch, e.Nodes, optStamp(e.ReportedAt)})
	}
	nvs := make([]wireNvidia, 0, len(p.NvidiaBranches))
	for _, e := range p.NvidiaBranches {
		nvs = append(nvs, wireNvidia{e.Branch, e.Codename, e.Arch, e.Nodes, optStamp(e.ReportedAt)})
	}
	raw, err := jsonord.Object([]jsonord.KV{
		{Key: keySchema, Val: PinsSchemaVersion},
		{Key: keyUpdated, Val: jsonord.Stamp(p.UpdatedAt)},
		{Key: keyLlama, Val: llama},
		{Key: keyWhisper, Val: whisper},
		{Key: keyAgent, Val: agent},
		{Key: keyCodename, Val: cns},
		{Key: keyNvidia, Val: nvs},
	}, p.extra)
	if err != nil {
		return nil, err
	}
	return jsonord.Indent(raw)
}

// sorted returns a copy with every list and node list in the canonical order.
func (p Pins) sorted() Pins {
	// Merge with itself normalises order and unions duplicates but also prunes; the writer must not
	// prune (it has no clock), so sort by hand.
	q := p
	q.LlamaCpp = append([]CommitPin(nil), p.LlamaCpp...)
	q.WhisperCpp = append([]CommitPin(nil), p.WhisperCpp...)
	q.Agent = append([]AgentPin(nil), p.Agent...)
	q.Codenames = append([]CodenamePin(nil), p.Codenames...)
	q.NvidiaBranches = append([]NvidiaPin(nil), p.NvidiaBranches...)
	for i := range q.LlamaCpp {
		q.LlamaCpp[i].Nodes = sortedCopy(q.LlamaCpp[i].Nodes)
	}
	for i := range q.WhisperCpp {
		q.WhisperCpp[i].Nodes = sortedCopy(q.WhisperCpp[i].Nodes)
	}
	for i := range q.Agent {
		q.Agent[i].Nodes = sortedCopy(q.Agent[i].Nodes)
	}
	for i := range q.Codenames {
		q.Codenames[i].Nodes = sortedCopy(q.Codenames[i].Nodes)
	}
	for i := range q.NvidiaBranches {
		q.NvidiaBranches[i].Nodes = sortedCopy(q.NvidiaBranches[i].Nodes)
	}
	sort.Slice(q.LlamaCpp, func(i, j int) bool { return q.LlamaCpp[i].Commit < q.LlamaCpp[j].Commit })
	sort.Slice(q.WhisperCpp, func(i, j int) bool { return q.WhisperCpp[i].Commit < q.WhisperCpp[j].Commit })
	sort.Slice(q.Agent, func(i, j int) bool { return q.Agent[i].Tag < q.Agent[j].Tag })
	sort.Slice(q.Codenames, func(i, j int) bool {
		x, y := q.Codenames[i], q.Codenames[j]
		return x.Codename < y.Codename || x.Codename == y.Codename && x.Arch < y.Arch
	})
	sort.Slice(q.NvidiaBranches, func(i, j int) bool {
		x, y := q.NvidiaBranches[i], q.NvidiaBranches[j]
		if x.Branch != y.Branch {
			return x.Branch < y.Branch
		}
		if x.Codename != y.Codename {
			return x.Codename < y.Codename
		}
		return x.Arch < y.Arch
	})
	return q
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// ParsePins reads the bytes of a pins.json strictly: valid JSON object, schema_version 1 (checked
// before anything else, so a newer file is reported as such), every entry valid. Unknown top-level
// keys are kept for the next Encode; unknown keys inside entries are ignored.
func ParsePins(data []byte) (Pins, error) {
	if len(data) > maxPinsBytes {
		return Pins{}, fmt.Errorf("%w: file is larger than %d bytes", ErrPinsCorrupt, maxPinsBytes)
	}
	var head struct {
		SchemaVersion *int `json:"schema_version"`
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return Pins{}, fmt.Errorf("%w: %v", ErrPinsCorrupt, err)
	}
	if raw, ok := probe[keySchema]; ok {
		if err := json.Unmarshal(raw, &head.SchemaVersion); err != nil {
			return Pins{}, fmt.Errorf("%w: schema_version: %v", ErrPinsCorrupt, err)
		}
	}
	if head.SchemaVersion == nil {
		return Pins{}, fmt.Errorf("%w: schema_version is missing", ErrPinsCorrupt)
	}
	if *head.SchemaVersion != PinsSchemaVersion {
		return Pins{}, fmt.Errorf("%w: schema_version is %d, this agent reads %d", ErrPinsSchema, *head.SchemaVersion, PinsSchemaVersion)
	}
	var (
		updated jsonord.Stamp
		llama   []wireCommit
		whisper []wireCommit
		agent   []wireAgent
		cns     []wireCodename
		nvs     []wireNvidia
		version int
	)
	extra, err := jsonord.Decode(data, map[string]any{
		keySchema: &version, keyUpdated: &updated,
		keyLlama: &llama, keyWhisper: &whisper, keyAgent: &agent, keyCodename: &cns, keyNvidia: &nvs,
	})
	if err != nil {
		return Pins{}, fmt.Errorf("%w: %v", ErrPinsCorrupt, err)
	}
	p := Pins{UpdatedAt: stamp(time.Time(updated)), extra: extra}
	for _, e := range llama {
		p.LlamaCpp = append(p.LlamaCpp, CommitPin{e.Commit, e.Nodes, fromStamp(e.ReportedAt)})
	}
	for _, e := range whisper {
		p.WhisperCpp = append(p.WhisperCpp, CommitPin{e.Commit, e.Nodes, fromStamp(e.ReportedAt)})
	}
	for _, e := range agent {
		p.Agent = append(p.Agent, AgentPin{e.Tag, e.Nodes, fromStamp(e.ReportedAt)})
	}
	for _, e := range cns {
		p.Codenames = append(p.Codenames, CodenamePin{e.Codename, e.Arch, e.Nodes, fromStamp(e.ReportedAt)})
	}
	for _, e := range nvs {
		p.NvidiaBranches = append(p.NvidiaBranches, NvidiaPin{e.Branch, e.Codename, e.Arch, e.Nodes, fromStamp(e.ReportedAt)})
	}
	if err := p.Validate(); err != nil {
		return Pins{}, err
	}
	return p, nil
}

// ---------------------------------------------------------------------------------------------
// Files

// ReadPins reads and validates the pins file at filePath. A missing file satisfies
// errors.Is(err, fs.ErrNotExist) and is the normal "nothing pinned yet".
func ReadPins(fsys config.FS, filePath string) (Pins, error) {
	data, err := fsys.ReadFile(filePath)
	if err != nil {
		return Pins{}, err
	}
	p, err := ParsePins(data)
	if err != nil {
		return Pins{}, fmt.Errorf("%s: %w", filePath, err)
	}
	return p, nil
}

// WritePins validates p and replaces the pins file atomically (temp file, fsync, rename, fsync of
// the directory), mode 0644. A failure leaves the file untouched. An error wrapping
// config.ErrNotDurable means the file was replaced but the directory sync failed.
func WritePins(fsys config.FS, filePath string, p Pins) error {
	data, err := p.Encode()
	if err != nil {
		return err
	}
	return config.WriteFileAtomicFS(fsys, filePath, data, 0o644, -1, -1)
}

// UpdatePins is the read-merge-write of one root: it reads the pins at filePath (a missing file is
// empty), merges own into it as of now and writes the result back, returning what it wrote. A damaged
// or newer file is an error (LAI-KIT-045) and is left exactly as it is: the caller reports the code
// and the file is replaced by the next catalog release's copy, never silently overwritten here.
func UpdatePins(fsys config.FS, filePath string, own Pins, now time.Time) (Pins, error) {
	cur, err := ReadPins(fsys, filePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Pins{}, err
	}
	merged := Merge(cur, own, now)
	if err := WritePins(fsys, filePath, merged); err != nil {
		return Pins{}, err
	}
	return merged, nil
}

// IsPinsDamaged reports whether err is one of the LAI-KIT-045 errors (as opposed to a missing file
// or a drive I/O error).
func IsPinsDamaged(err error) bool {
	return errors.Is(err, ErrPinsCorrupt) || errors.Is(err, ErrPinsSchema)
}

// String is a one-line summary for logs.
func (p Pins) String() string {
	return fmt.Sprintf("pins{llama %d, whisper %d, agent %d, codenames %d, nvidia %d, nodes %s}",
		len(p.LlamaCpp), len(p.WhisperCpp), len(p.Agent), len(p.Codenames), len(p.NvidiaBranches), strings.Join(p.NodesOf(), ","))
}
