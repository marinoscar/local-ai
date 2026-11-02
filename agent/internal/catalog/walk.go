package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// The strict walker. yaml.v3's KnownFields(true) rejects unknown keys but names neither the YAML
// path nor the file, so the schema is checked here against the node tree instead: every unknown
// key, wrong type, duplicate key, alias or missing required field becomes one LAI-CAT-010 problem
// with its path (models[3].source.revision) and the line of the offending node.
//
// Struct tags drive it: `yaml:"name"` names the key, `catalog:"required"` makes it mandatory (a
// null or empty value counts as missing) and `catalog:"keys=<regex>"` on a `yaml:",inline"` map
// admits extra keys that match the regex (summary_<lang>).

var unmarshalerType = reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem()

type fieldSpec struct {
	name     string
	index    int
	required bool
}

type structSpec struct {
	fields []fieldSpec
	byName map[string]fieldSpec
	inline int // field index of the inline map, -1 when none
	keys   *regexp.Regexp
}

var specCache sync.Map // reflect.Type -> *structSpec

func specOf(t reflect.Type) *structSpec {
	if v, ok := specCache.Load(t); ok {
		return v.(*structSpec)
	}
	s := &structSpec{byName: map[string]fieldSpec{}, inline: -1}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		yamlTag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(yamlTag, ",")
		var required bool
		var keysRe *regexp.Regexp
		for _, opt := range strings.Split(f.Tag.Get("catalog"), ";") {
			switch {
			case opt == "required":
				required = true
			case strings.HasPrefix(opt, "keys="):
				keysRe = regexp.MustCompile(strings.TrimPrefix(opt, "keys="))
			}
		}
		if opts == "inline" {
			s.inline, s.keys = i, keysRe
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fs := fieldSpec{name: name, index: i, required: required}
		s.fields = append(s.fields, fs)
		s.byName[name] = fs
	}
	specCache.Store(t, s)
	return s
}

// walker checks one document and reports through add (code is always LAI-CAT-010).
type walker struct {
	add func(path, msg string)
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func indexPath(parent string, i int) string { return parent + "[" + strconv.Itoa(i) + "]" }

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" }

// describe says what a node is, for "expected X, got Y".
func describe(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.AliasNode:
		return "an alias"
	}
	switch n.ShortTag() {
	case "!!str":
		return "a string"
	case "!!int":
		return "a whole number"
	case "!!float":
		return "a number"
	case "!!bool":
		return "true/false"
	case "!!null":
		return "an empty value"
	}
	return "a " + strings.TrimPrefix(n.ShortTag(), "!!")
}

func (w *walker) walk(n *yaml.Node, t reflect.Type, path string) {
	if n.Kind == yaml.AliasNode {
		w.add(path, "YAML aliases are not allowed in the catalog")
		return
	}
	if isNull(n) {
		return // presence and emptiness of required fields is checked by the parent
	}
	if reflect.PointerTo(t).Implements(unmarshalerType) && t.Kind() != reflect.Ptr {
		v := reflect.New(t)
		if err := n.Decode(v.Interface()); err != nil {
			w.add(path, cleanYAMLError(err))
		}
		return
	}
	switch t.Kind() {
	case reflect.Ptr:
		w.walk(n, t.Elem(), path)
	case reflect.Struct:
		w.walkStruct(n, t, path)
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			w.add(path, fmt.Sprintf("expected a list, got %s", describe(n)))
			return
		}
		for i, c := range n.Content {
			w.walk(c, t.Elem(), indexPath(path, i))
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			w.add(path, fmt.Sprintf("expected a mapping, got %s", describe(n)))
			return
		}
		seen := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			kp := joinPath(path, k.Value)
			if first, dup := seen[k.Value]; dup {
				w.add(kp, fmt.Sprintf("duplicate key %q (first on line %d)", k.Value, first))
				continue
			}
			seen[k.Value] = k.Line
			w.walk(v, t.Elem(), kp)
		}
	case reflect.String:
		w.scalar(n, path, "!!str", "a string")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		w.scalar(n, path, "!!int", "a whole number")
	case reflect.Float32, reflect.Float64:
		w.scalar(n, path, "!!int|!!float", "a number")
	case reflect.Bool:
		w.scalar(n, path, "!!bool", "true or false")
	}
}

func (w *walker) scalar(n *yaml.Node, path, tags, want string) {
	if n.Kind != yaml.ScalarNode {
		w.add(path, fmt.Sprintf("expected %s, got %s", want, describe(n)))
		return
	}
	for _, tag := range strings.Split(tags, "|") {
		if n.ShortTag() == tag {
			return
		}
	}
	msg := fmt.Sprintf("expected %s, got %s", want, describe(n))
	if tags == "!!str" {
		msg += " (quote it if it is text)"
	}
	w.add(path, msg)
}

func (w *walker) walkStruct(n *yaml.Node, t reflect.Type, path string) {
	if n.Kind != yaml.MappingNode {
		w.add(path, fmt.Sprintf("expected a mapping (key: value pairs), got %s", describe(n)))
		return
	}
	spec := specOf(t)
	seen := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		kp := joinPath(path, k.Value)
		if k.Kind != yaml.ScalarNode {
			w.add(path, "mapping keys must be plain strings")
			continue
		}
		if prev, dup := seen[k.Value]; dup {
			w.add(kp, fmt.Sprintf("duplicate key %q (first on line %d)", k.Value, prev.Line))
			continue
		}
		seen[k.Value] = k
		if f, ok := spec.byName[k.Value]; ok {
			w.walk(v, t.Field(f.index).Type, kp)
			continue
		}
		if spec.inline >= 0 && spec.keys != nil && spec.keys.MatchString(k.Value) {
			w.scalar(v, kp, "!!str", "a string")
			continue
		}
		w.add(kp, fmt.Sprintf("unknown key %q", k.Value))
	}
	for _, f := range spec.fields {
		if !f.required {
			continue
		}
		fp := joinPath(path, f.name)
		k, ok := seen[f.name]
		if !ok {
			w.add(fp, fmt.Sprintf("missing required field %q", f.name))
			continue
		}
		v := valueOf(n, k)
		switch {
		case isNull(v):
			w.add(fp, fmt.Sprintf("missing required field %q (it is empty)", f.name))
		case t.Field(f.index).Type.Kind() == reflect.String && v.Kind == yaml.ScalarNode && v.Value == "":
			w.add(fp, fmt.Sprintf("field %q must not be empty", f.name))
		}
	}
}

// valueOf returns the value node that goes with key node k in mapping m.
func valueOf(m, k *yaml.Node) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i] == k {
			return m.Content[i+1]
		}
	}
	return k
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// cleanYAMLError turns "yaml: line 3: did not find expected key" into a message without the
// "yaml:" prefix, and reports the line it names (0 when none).
func cleanYAMLError(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		msgs := make([]string, len(te.Errors))
		for i, m := range te.Errors {
			msgs[i] = lineRe.ReplaceAllString(m, "")
		}
		return oneLine(strings.Join(msgs, "; "))
	}
	return oneLine(strings.TrimPrefix(err.Error(), "yaml: "))
}

var lineRe = regexp.MustCompile(`^line \d+: `)

func errorLine(err error) int {
	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// lineIndex maps every YAML path of a document to the line of its key (or list item). It is generic
// (it does not know the schema), so any problem path can be given a line.
func lineIndex(root *yaml.Node) map[string]int {
	idx := map[string]int{"": root.Line}
	var rec func(n *yaml.Node, path string)
	rec = func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				kp := joinPath(path, k.Value)
				if _, ok := idx[kp]; !ok {
					idx[kp] = k.Line
				}
				rec(v, kp)
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				cp := indexPath(path, i)
				idx[cp] = c.Line
				rec(c, cp)
			}
		}
	}
	rec(root, "")
	return idx
}

// lineFor finds the line of path, falling back to the nearest existing parent (a missing key has
// no line of its own; its mapping does).
func lineFor(idx map[string]int, path string) int {
	for {
		if l, ok := idx[path]; ok {
			return l
		}
		cut := strings.LastIndexAny(path, ".[")
		if cut < 0 {
			return idx[""]
		}
		path = path[:cut]
	}
}

// parsedFile is a strictly decoded YAML file.
type parsedFile[T any] struct {
	Value *T
	Lines map[string]int
	// Dirty holds the entries (models[3], content[0], or "" for the file's own fields) that have a
	// schema problem; the semantic rules skip them so one mistake is reported once.
	Dirty map[string]bool
}

// entryKey is the list entry a path belongs to: "models[3]" for "models[3].source.revision".
func entryKey(path string) string {
	if i := strings.Index(path, "]"); i >= 0 {
		return path[:i+1]
	}
	return ""
}

// decodeStrict parses data, walks it against T and returns the typed value. Schema problems go to
// add (as LAI-CAT-010). The result is non-nil whenever the YAML parsed, even with schema problems.
func decodeStrict[T any](data []byte, add func(path, msg string, line int)) *parsedFile[T] {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			add("", "file is empty", 0)
		} else {
			add("", "YAML syntax error: "+cleanYAMLError(err), errorLine(err))
		}
		return nil
	}
	if len(doc.Content) == 0 {
		add("", "file is empty", 0)
		return nil
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		add("", "only one YAML document is allowed per file", extra.Line)
	}
	root := doc.Content[0]
	pf := &parsedFile[T]{Value: new(T), Lines: lineIndex(root), Dirty: map[string]bool{}}
	problems := 0
	w := &walker{add: func(path, msg string) {
		problems++
		pf.Dirty[entryKey(path)] = true
		add(path, msg, lineFor(pf.Lines, path))
	}}
	var zero T
	w.walk(root, reflect.TypeOf(zero), "")
	if err := root.Decode(pf.Value); err != nil && problems == 0 {
		add("", "cannot decode: "+cleanYAMLError(err), errorLine(err))
		pf.Dirty[""] = true
	}
	return pf
}
