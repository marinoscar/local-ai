package catalog

import "sort"

// EffectiveWorkloads is the node's workload set (catalog.md section 4, "Workloads"): the profile's
// workloads, where a node.yaml workload of the same name replaces the profile's one and a new name
// adds one. profile may be nil. The result is a fresh map; the model lists are copies.
func EffectiveWorkloads(profile *Profile, node map[string]Workload) map[string]Workload {
	out := map[string]Workload{}
	if profile != nil {
		for name, w := range profile.Workloads {
			w.Models = append([]string(nil), w.Models...)
			out[name] = w
		}
	}
	for name, w := range node {
		w.Models = append([]string(nil), w.Models...)
		out[name] = w
	}
	return out
}

// WorkloadNames returns the names of ws, sorted.
func WorkloadNames(ws map[string]Workload) []string {
	out := make([]string, 0, len(ws))
	for n := range ws {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
