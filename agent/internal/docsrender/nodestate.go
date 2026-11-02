package docsrender

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultEmergencyNumber is portal.emergency_number when node.yaml does not set one (survival-portal.md 3.4).
const DefaultEmergencyNumber = "911"

// UnconfiguredNode is the node id of the agent's safe defaults (agent.md 3.4); the card uses it when
// nothing says which node this is.
const UnconfiguredNode = "unconfigured"

// StateSources are the files the node state is read from. Everything is best effort: a missing or
// unreadable file leaves the field empty and the renderer carries on, because the manuals must come
// out on a node whose configuration is damaged.
type StateSources struct {
	// NodeYAML is /etc/localai/node.yaml.
	NodeYAML string
	// InstallState is /var/lib/localai/install/state.json.
	InstallState string
	// ContentRoots are the content roots (/srv/localai and a mounted drive's localai/) whose
	// models/<id>/MANIFEST.json files say which brains are installed.
	ContentRoots []string
	// CatalogDirs are catalog directories to read CATALOG_VERSION and display names from, first
	// valid wins (/etc/localai/catalog, then <root>/kit/catalog of each content root).
	CatalogDirs []string
}

// LoadNodeState reads what the sources know.
func LoadNodeState(s StateSources) NodeState {
	var st NodeState
	if b, err := os.ReadFile(s.NodeYAML); err == nil {
		var n struct {
			Node struct {
				ID string `yaml:"id"`
			} `yaml:"node"`
			Portal struct {
				EmergencyNumber string `yaml:"emergency_number"`
			} `yaml:"portal"`
		}
		if yaml.Unmarshal(b, &n) == nil {
			st.NodeID = strings.TrimSpace(n.Node.ID)
			st.EmergencyNumber = strings.TrimSpace(n.Portal.EmergencyNumber)
		}
	}
	if st.EmergencyNumber == "" {
		st.EmergencyNumber = DefaultEmergencyNumber
	}
	if b, err := os.ReadFile(s.InstallState); err == nil {
		var v any
		if json.Unmarshal(b, &v) == nil {
			st.DesktopUser = findKey(v, "desktop_user", 4)
		}
	}

	names := map[string]string{} // model id -> display name
	for _, d := range s.CatalogDirs {
		if st.CatalogVersion == "" {
			if b, err := os.ReadFile(filepath.Join(d, "CATALOG_VERSION")); err == nil {
				st.CatalogVersion = strings.TrimSpace(string(b))
			}
		}
		if b, err := os.ReadFile(filepath.Join(d, "models.yaml")); err == nil {
			var m struct {
				Models []struct {
					ID          string `yaml:"id"`
					DisplayName string `yaml:"display_name"`
				} `yaml:"models"`
			}
			if yaml.Unmarshal(b, &m) == nil {
				for _, x := range m.Models {
					if _, ok := names[x.ID]; !ok && x.DisplayName != "" {
						names[x.ID] = x.DisplayName
					}
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, root := range s.ContentRoots {
		matches, _ := filepath.Glob(filepath.Join(root, "models", "*", "MANIFEST.json"))
		for _, mp := range matches {
			b, err := os.ReadFile(mp)
			if err != nil {
				continue
			}
			var m struct {
				ItemID         string `json:"item_id"`
				State          string `json:"state"`
				CatalogVersion string `json:"catalog_version"`
			}
			if json.Unmarshal(b, &m) != nil || m.State != "verified" {
				continue
			}
			id := m.ItemID
			if id == "" {
				id = filepath.Base(filepath.Dir(mp))
			}
			if st.CatalogVersion == "" {
				st.CatalogVersion = m.CatalogVersion
			}
			name := names[id]
			if name == "" {
				name = id
			}
			if !seen[name] {
				seen[name] = true
				st.Brains = append(st.Brains, name)
			}
		}
	}
	sort.Strings(st.Brains)
	if st.CatalogVersion == "" {
		st.CatalogVersion = "unknown"
	}
	return st
}

// findKey looks for a string value under key in decoded JSON, to the given depth.
func findKey(v any, key string, depth int) string {
	if depth < 0 {
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s := findKey(t[k], key, depth-1); s != "" {
				return s
			}
		}
	case []any:
		for _, x := range t {
			if s := findKey(x, key, depth-1); s != "" {
				return s
			}
		}
	}
	return ""
}
