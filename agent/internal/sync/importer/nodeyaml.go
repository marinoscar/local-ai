package importer

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Updater is the part of *config.Store that edits node.yaml: it hands the callback the yaml tree,
// keeps the comments, saves the previous file as node.yaml.bak and replaces the file atomically.
type Updater interface {
	Update(fn func(root *yaml.Node) error) error
}

// NodeYAML is the ProfileEditor over node.yaml.
type NodeYAML struct{ Store Updater }

var _ ProfileEditor = NodeYAML{}

// AddItems implements ProfileEditor: the ids go to profile_overrides.add_models and add_content (sorted,
// no duplicates, what was there stays) and leave remove_models and remove_content, because a removal
// wins over an addition in the desired set and the person has just asked for the item.
func (n NodeYAML) AddItems(models, content []string) error {
	if n.Store == nil {
		return fmt.Errorf("importer: no node.yaml to change")
	}
	return n.Store.Update(func(root *yaml.Node) error {
		for _, set := range []struct {
			add, remove string
			ids         []string
		}{
			{"add_models", "remove_models", models},
			{"add_content", "remove_content", content},
		} {
			if len(set.ids) == 0 {
				continue
			}
			cur, err := list(root, set.add)
			if err != nil {
				return err
			}
			merged := sorted(appendUnique(cur, set.ids))
			if err := config.SetKey(root, merged, "profile_overrides", set.add); err != nil {
				return err
			}
			rm, err := list(root, set.remove)
			if err != nil {
				return err
			}
			if kept := without(rm, set.ids); len(kept) != len(rm) {
				if err := config.SetKey(root, kept, "profile_overrides", set.remove); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// list reads profile_overrides.<key> as a list of strings; a missing or null key is empty.
func list(root *yaml.Node, key string) ([]string, error) {
	n := config.LookupKey(root, "profile_overrides", key)
	if n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null") {
		return nil, nil
	}
	var out []string
	if err := n.Decode(&out); err != nil {
		return nil, fmt.Errorf("importer: profile_overrides.%s is not a list of ids: %w", key, err)
	}
	return out, nil
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
