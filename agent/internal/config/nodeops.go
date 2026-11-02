package config

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// The helpers in this file edit the yaml.Node tree that Store.Update hands to its callback. They
// work on the tree, never on the typed Config, because re-marshalling a struct would drop every
// comment and reorder keys. root is the top-level mapping node.

// LookupKey returns the value node at path under root, or nil when any step is missing.
func LookupKey(root *yaml.Node, path ...string) *yaml.Node {
	n := root
	for _, key := range path {
		n = resolveAlias(n)
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		if n = mapGet(n, key); n == nil {
			return nil
		}
	}
	return n
}

// SetKey sets the value at path under root, creating the intermediate mappings (and the key) when
// they are missing. Comments on an existing key stay, an existing key keeps its position, a new key
// is appended to its mapping. value is anything yaml.v3 can encode: a string, a number, a bool, a
// slice, a map, or nil for null.
func SetKey(root *yaml.Node, value any, path ...string) error {
	if len(path) == 0 {
		return fmt.Errorf("config: SetKey needs a path")
	}
	m := root
	for i, key := range path[:len(path)-1] {
		if m.Kind != yaml.MappingNode {
			return fmt.Errorf("config: %s is not a group of settings", strings.Join(path[:i], "."))
		}
		keyNode, child := mapGetPair(m, key)
		switch {
		case child == nil:
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapAppend(m, key, child)
		case child.Kind == yaml.AliasNode:
			return fmt.Errorf("config: %s uses a YAML alias; edit node.yaml by hand", strings.Join(path[:i+1], "."))
		case isNull(child):
			*child = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", LineComment: child.LineComment}
			blockify(keyNode, child)
		case child.Kind != yaml.MappingNode:
			return fmt.Errorf("config: %s is not a group of settings", strings.Join(path[:i+1], "."))
		case len(child.Content) == 0:
			blockify(keyNode, child) // {} about to receive keys
		}
		m = child
	}
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("config: %s is not a group of settings", strings.Join(path[:len(path)-1], "."))
	}
	var nv yaml.Node
	if err := nv.Encode(value); err != nil {
		return fmt.Errorf("config: encode value for %s: %w", strings.Join(path, "."), err)
	}
	last := path[len(path)-1]
	old := mapGet(m, last)
	if old == nil {
		mapAppend(m, last, &nv)
		return nil
	}
	if old.Kind == yaml.AliasNode {
		return fmt.Errorf("config: %s uses a YAML alias; edit node.yaml by hand", strings.Join(path, "."))
	}
	nv.HeadComment, nv.LineComment, nv.FootComment = old.HeadComment, old.LineComment, old.FootComment
	switch {
	case old.Kind == yaml.ScalarNode && nv.Kind == yaml.ScalarNode && old.Tag == "!!str" && nv.Tag == "!!str":
		nv.Style = old.Style // keep "quoted" text quoted
	case old.Kind == yaml.SequenceNode && nv.Kind == yaml.SequenceNode:
		nv.Style = old.Style // keep [flow, lists] flow
	}
	*old = nv
	return nil
}

// DeleteKey removes the key at path and reports whether it existed.
func DeleteKey(root *yaml.Node, path ...string) bool {
	if len(path) == 0 {
		return false
	}
	parent := root
	if len(path) > 1 {
		parent = LookupKey(root, path[:len(path)-1]...)
	}
	parent = resolveAlias(parent)
	if parent == nil || parent.Kind != yaml.MappingNode {
		return false
	}
	last := path[len(path)-1]
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == last {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return true
		}
	}
	return false
}

// blockify turns an empty mapping into block style before it receives keys. Its trailing comment
// moves onto the key, because yaml.v3 prints the comment of a block mapping value on the next
// key's line.
func blockify(key, v *yaml.Node) {
	v.Style &^= yaml.FlowStyle
	if key != nil && v.LineComment != "" && key.LineComment == "" {
		key.LineComment, v.LineComment = v.LineComment, ""
	}
}

func mapGetPair(m *yaml.Node, key string) (k, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapAppend(m *yaml.Node, key string, v *yaml.Node) {
	if len(m.Content) == 0 {
		m.Style &^= yaml.FlowStyle // an empty {} gets block style once it has content
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// encodeDocument renders the tree with a 2-space indent and puts back the blank lines that
// separated top-level blocks in orig (yaml.v3 drops blank lines; comments and key order survive).
func encodeDocument(doc *yaml.Node, orig []byte) ([]byte, error) {
	moveStrandedComments(doc)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return restoreBlankLines(doc, orig, buf.Bytes()), nil
}

// moveStrandedComments works around a yaml.v3 emitter bug: the comment after "key:" is dropped (or
// printed on the next key's line) when the value is a flow list or map that starts on the following
// line, as cloud.allow does in the agent.md example. The comment moves to the end of the value.
func moveStrandedComments(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.LineComment != "" && v.Style&yaml.FlowStyle != 0 && (v.Kind == yaml.SequenceNode || v.Kind == yaml.MappingNode) {
				if v.LineComment == "" {
					v.LineComment = k.LineComment
				} else {
					v.LineComment = k.LineComment + " " + v.LineComment
				}
				k.LineComment = ""
			}
		}
	}
	for _, c := range n.Content {
		moveStrandedComments(c)
	}
}

// restoreBlankLines inserts a blank line before every top-level key (and the comment block above
// it) that had one in orig.
func restoreBlankLines(doc *yaml.Node, orig, out []byte) []byte {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode || len(orig) == 0 {
		return out
	}
	origLines := strings.Split(string(orig), "\n")
	spaced := map[string]bool{}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Line < 1 || k.Line > len(origLines) {
			continue
		}
		j := k.Line - 1
		for j > 0 && strings.HasPrefix(origLines[j-1], "#") {
			j--
		}
		if j > 0 && strings.TrimSpace(origLines[j-1]) == "" {
			spaced[k.Value] = true
		}
	}
	if len(spaced) == 0 {
		return out
	}
	lines := strings.Split(string(out), "\n")
	insert := map[int]bool{}
	for i, l := range lines {
		key, ok := topLevelKey(l)
		if !ok || !spaced[key] {
			continue
		}
		s := i
		for s > 0 && strings.HasPrefix(lines[s-1], "#") {
			s--
		}
		if s > 0 && strings.TrimSpace(lines[s-1]) != "" {
			insert[s] = true
		}
	}
	var b strings.Builder
	for i, l := range lines {
		if insert[i] {
			b.WriteByte('\n')
		}
		b.WriteString(l)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

var topKeyRe = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*):(\s|$)`)

func topLevelKey(line string) (string, bool) {
	m := topKeyRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}
