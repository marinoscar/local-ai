package docsrender

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Inventory handling (aictl.md 4.22 note 2, security-model.md section 5). There is no hand-kept table
// from placeholder to field: inventory.example.yaml is loaded, every scalar whose value is a
// placeholder is paired with the same YAML path in inventory.local.yaml, and list entries are
// matched by their selector key (id; label and name for lists that have no id, such as ark_drives).

// InventoryFile is the file name of the real inventory.
const InventoryFile = "inventory.local.yaml"

// ExampleFile is the file name of the committed schema with the placeholders.
const ExampleFile = "inventory.example.yaml"

// selectorKeys are the keys that identify a list entry, in order of preference.
var selectorKeys = []string{"id", "label", "name"}

type segKind int

const (
	segKey segKind = iota // a mapping key
	segSel                // a list entry picked by key=value
	segIdx                // a list entry picked by position (no selector key)
)

type seg struct {
	kind segKind
	key  string // segKey: the key; segSel: the selector key
	val  string // segSel: the selector value
	idx  int    // segIdx
}

func (s seg) String() string {
	switch s.kind {
	case segSel:
		return "[" + s.key + "=" + s.val + "]"
	case segIdx:
		return "[" + strconv.Itoa(s.idx) + "]"
	}
	return s.key
}

func pathString(p []seg) string {
	var b strings.Builder
	for i, s := range p {
		if i > 0 && s.kind == segKey {
			b.WriteByte('.')
		}
		b.WriteString(s.String())
	}
	return b.String()
}

// Pair is one placeholder of inventory.example.yaml and, when the local inventory has the value, its
// real value.
type Pair struct {
	Name   string // the placeholder name without brackets
	Path   string // for example nodes[id=ai-n1].tailnet_ip
	Field  string // the last key of the path: tailnet_ip
	NodeID string // the id of the nodes[] entry the path goes through, or ""
	Value  string
	Has    bool // Value came from the local inventory
	Secret bool // Field is in secret_fields, or the name is on the built-in list
}

// Pairs is the result of pairing the example with the local inventory (nil local: no values).
type Pairs struct {
	Pairs        []Pair
	SecretFields map[string]bool
	byName       map[string][]int
	// HasLocal says a local inventory was parsed.
	HasLocal bool
}

// PairInventory pairs example with local. local may be nil (no inventory: the pairs carry no values
// but the secret markers are still known). Malformed YAML is an error naming the file.
func PairInventory(example, local []byte) (*Pairs, error) {
	var ex yaml.Node
	if err := yaml.Unmarshal(example, &ex); err != nil {
		return nil, fmt.Errorf("%s: %w", ExampleFile, err)
	}
	var lc yaml.Node
	hasLocal := local != nil
	if hasLocal {
		if err := yaml.Unmarshal(local, &lc); err != nil {
			return nil, fmt.Errorf("%s: %w", InventoryFile, err)
		}
	}
	p := &Pairs{SecretFields: map[string]bool{}, byName: map[string][]int{}, HasLocal: hasLocal}
	exRoot := docRoot(&ex)
	if exRoot == nil || exRoot.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level must be a mapping", ExampleFile)
	}
	if sf := mapValue(exRoot, "secret_fields"); sf != nil && sf.Kind == yaml.SequenceNode {
		for _, n := range sf.Content {
			if n.Kind == yaml.ScalarNode {
				p.SecretFields[n.Value] = true
			}
		}
	}
	lcRoot := docRoot(&lc)
	var walk func(n *yaml.Node, path []seg)
	walk = func(n *yaml.Node, path []seg) {
		n = deref(n)
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i+1], append(path[:len(path):len(path)], seg{kind: segKey, key: n.Content[i].Value}))
			}
		case yaml.SequenceNode:
			for i, item := range n.Content {
				walk(item, append(path[:len(path):len(path)], entrySeg(deref(item), i)))
			}
		case yaml.ScalarNode:
			m := wholePlaceholderRE.FindStringSubmatch(n.Value)
			if m == nil {
				return
			}
			pr := Pair{Name: m[1], Path: pathString(path)}
			for _, s := range path {
				if s.kind == segKey {
					pr.Field = s.key
				}
				if s.kind == segSel && s.key == "id" {
					pr.NodeID = s.val
				}
			}
			if hasLocal && lcRoot != nil {
				if v, ok := lookup(lcRoot, path); ok && !placeholderRE.MatchString(v) {
					pr.Value, pr.Has = v, true
				}
			}
			p.byName[pr.Name] = append(p.byName[pr.Name], len(p.Pairs))
			p.Pairs = append(p.Pairs, pr)
		}
	}
	walk(exRoot, nil)
	for i := range p.Pairs {
		pr := &p.Pairs[i]
		pr.Secret = p.SecretFields[pr.Field] || builtinSecret(pr.Name)
	}
	return p, nil
}

// entrySeg picks how a list entry is addressed.
func entrySeg(item *yaml.Node, idx int) seg {
	if item.Kind == yaml.MappingNode {
		for _, k := range selectorKeys {
			if v := mapValue(item, k); v != nil && v.Kind == yaml.ScalarNode && v.Value != "" {
				return seg{kind: segSel, key: k, val: v.Value}
			}
		}
	}
	return seg{kind: segIdx, idx: idx}
}

func docRoot(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return deref(n.Content[0])
	}
	if n.Kind == 0 {
		return nil
	}
	return deref(n)
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return deref(m.Content[i+1])
		}
	}
	return nil
}

// lookup follows path in the local inventory and returns the scalar there.
func lookup(root *yaml.Node, path []seg) (string, bool) {
	cur := deref(root)
	for _, s := range path {
		if cur == nil {
			return "", false
		}
		switch s.kind {
		case segKey:
			cur = mapValue(cur, s.key)
		case segSel:
			var found *yaml.Node
			if cur.Kind == yaml.SequenceNode {
				for _, item := range cur.Content {
					if v := mapValue(item, s.key); v != nil && v.Kind == yaml.ScalarNode && v.Value == s.val {
						found = deref(item)
						break
					}
				}
			}
			cur = found
		case segIdx:
			if cur.Kind == yaml.SequenceNode && s.idx < len(cur.Content) {
				cur = deref(cur.Content[s.idx])
			} else {
				cur = nil
			}
		}
	}
	if cur == nil || cur.Kind != yaml.ScalarNode || cur.Value == "" {
		return "", false
	}
	return cur.Value, true
}

// Names returns every placeholder name of the example, sorted.
func (p *Pairs) Names() []string {
	out := make([]string, 0, len(p.byName))
	for n := range p.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// IsSecret reports whether a placeholder is secret: on the built-in list, or paired with a field named
// in secret_fields. A name the example does not know is secret only by the built-in list.
func (p *Pairs) IsSecret(name string) bool {
	if builtinSecret(name) {
		return true
	}
	if p == nil {
		return false
	}
	for _, i := range p.byName[name] {
		if p.Pairs[i].Secret {
			return true
		}
	}
	return false
}

// Resolve returns the value of a placeholder for the node nodeID (may be ""). A placeholder paired
// under several nodes (<DESKTOP_USER>) takes this node's value; with no node, or a node without that
// field, it resolves only when every node that has a value agrees. The second result is false when
// the inventory has no value.
func (p *Pairs) Resolve(name, nodeID string) (string, bool) {
	if p == nil {
		return "", false
	}
	idxs := p.byName[name]
	if nodeID != "" {
		for _, i := range idxs {
			if pr := p.Pairs[i]; pr.NodeID == nodeID && pr.Has {
				return pr.Value, true
			}
		}
	}
	value, ok := "", false
	for _, i := range idxs {
		pr := p.Pairs[i]
		if !pr.Has {
			continue
		}
		if ok && pr.Value != value {
			return "", false
		}
		value, ok = pr.Value, true
	}
	return value, ok
}

// Values returns every placeholder that has a value for nodeID.
func (p *Pairs) Values(nodeID string) map[string]string {
	out := map[string]string{}
	if p == nil {
		return out
	}
	for name := range p.byName {
		if v, ok := p.Resolve(name, nodeID); ok {
			out[name] = v
		}
	}
	return out
}

// NodeIDByMachine returns the id of the node whose "machine" field equals host (the tailnet machine
// name), or "".
func NodeIDByMachine(local []byte, host string) string {
	var doc yaml.Node
	if host == "" || yaml.Unmarshal(local, &doc) != nil {
		return ""
	}
	nodes := mapValue(docRoot(&doc), "nodes")
	if nodes == nil || nodes.Kind != yaml.SequenceNode {
		return ""
	}
	for _, n := range nodes.Content {
		m, id := mapValue(n, "machine"), mapValue(n, "id")
		if m != nil && id != nil && strings.EqualFold(m.Value, host) {
			return id.Value
		}
	}
	return ""
}

// InventoryLookup says where to look for inventory.local.yaml.
type InventoryLookup struct {
	// Explicit is the --inventory value ("" when not given).
	Explicit string
	// EtcDir is /etc/localai.
	EtcDir string
	// DriveRoots are the mount points of mounted ark drives.
	DriveRoots []string
	// Cwd is the current directory.
	Cwd string
}

// FindInventory returns the first inventory file that exists: the explicit one, else
// <EtcDir>/inventory.local.yaml, else <drive>/inventory.local.yaml, else <cwd>/inventory.local.yaml.
// An empty result means none was found (placeholders stay, a warning is raised).
func FindInventory(l InventoryLookup) string {
	if l.Explicit != "" {
		if isFile(l.Explicit) {
			return l.Explicit
		}
		return ""
	}
	var cands []string
	if l.EtcDir != "" {
		cands = append(cands, filepath.Join(l.EtcDir, InventoryFile))
	}
	for _, d := range l.DriveRoots {
		cands = append(cands, filepath.Join(d, InventoryFile))
	}
	if l.Cwd != "" {
		cands = append(cands, filepath.Join(l.Cwd, InventoryFile))
	}
	for _, c := range cands {
		if isFile(c) {
			return c
		}
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
