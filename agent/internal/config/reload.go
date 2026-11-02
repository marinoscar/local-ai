package config

import (
	"reflect"
	"sort"
)

// This file classifies what a reload changed (agent.md 3.4, "Hot reload"):
//
//   - live: applied the moment the new file is accepted (profile*, cloud.allow, cloud.poll, sync.*,
//     ui.*, portal.*, logging.level, tailscale.*, llama.legacy_aliases, llama.load_timeout_seconds,
//     catalog.*, update.*, the display name and public host, and the companion blocks);
//   - restart the agent (CodeRestartNeeded, target "agent"): node.id, ports.*, cloud.url. The effective
//     config keeps the running value until the agent restarts;
//   - restart llama (CodeRestartNeeded, target "llama", never automatic): llama.extra_env (the
//     drop-in is regenerated, but env only reaches a new process) and llama.mode, build, commit,
//     backend and cuda_arch (they change the unit or the binary).

const (
	targetAgent = "agent"
	targetLlama = "llama"
)

// restartFields are the settings that need a restart, in report order.
var restartFields = []RestartNeed{
	{"node.id", targetAgent},
	{"ports.agent", targetAgent},
	{"ports.llama", targetAgent},
	{"ports.kiwix", targetAgent},
	{"ports.whisper", targetAgent},
	{"cloud.url", targetAgent},
	{"llama.mode", targetLlama},
	{"llama.build", targetLlama},
	{"llama.commit", targetLlama},
	{"llama.backend", targetLlama},
	{"llama.cuda_arch", targetLlama},
	{"llama.extra_env", targetLlama},
}

// pinRestartRequired returns cfg with the agent-restart settings forced back to the running values.
func pinRestartRequired(cfg, running Config) Config {
	cfg.Node.ID = running.Node.ID
	cfg.Ports = running.Ports
	cfg.Cloud.URL = running.Cloud.URL
	cfg.SafeDefaults = running.SafeDefaults
	return cfg
}

// pendingRestarts lists the agent-restart settings where the file differs from the running agent.
func pendingRestarts(running, file Config) []RestartNeed {
	a, b := leafMap(running), leafMap(file)
	var out []RestartNeed
	for _, f := range restartFields {
		if f.Target == targetAgent && !deepEqualLoose(a[f.Path], b[f.Path]) {
			out = append(out, f)
		}
	}
	return out
}

// restartsRaised lists the restarts a file change (oldFile to newFile) raised: agent settings that
// changed and now differ from the running agent, and llama settings that changed.
func restartsRaised(oldFile, newFile, running Config) []RestartNeed {
	o, n, r := leafMap(oldFile), leafMap(newFile), leafMap(running)
	var out []RestartNeed
	for _, f := range restartFields {
		if deepEqualLoose(o[f.Path], n[f.Path]) {
			continue
		}
		if f.Target == targetAgent && deepEqualLoose(n[f.Path], r[f.Path]) {
			continue // changed back to the value the agent runs with
		}
		out = append(out, f)
	}
	return out
}

func equalConfig(a, b Config) bool { return len(changedPaths(a, b)) == 0 }

func equalAny(a, b any) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.Kind() == reflect.Slice && bv.Kind() == reflect.Slice && av.Len() == 0 && bv.Len() == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// changedPaths lists the dotted paths whose value differs between a and b, sorted.
func changedPaths(a, b Config) []string {
	am, bm := leafMap(a), leafMap(b)
	seen := map[string]bool{}
	var out []string
	for k, av := range am {
		seen[k] = true
		if bv, ok := bm[k]; !ok || !deepEqualLoose(av, bv) {
			out = append(out, k)
		}
	}
	for k := range bm {
		if !seen[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// deepEqualLoose is DeepEqual, except that nil and empty slices or maps are equal (a default empty
// extra_env and one parsed from "{}" are the same setting).
func deepEqualLoose(a, b any) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.IsValid() && bv.IsValid() && av.Kind() == bv.Kind() &&
		(av.Kind() == reflect.Slice || av.Kind() == reflect.Map) && av.Len() == 0 && bv.Len() == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// leafMap flattens a Config into dotted path -> value. Struct fields recurse; slices, maps and
// pointers are leaves. Owned blocks appear under their block name as YAML text.
func leafMap(c Config) map[string]any {
	out := map[string]any{}
	walkLeaves(reflect.ValueOf(c), "", out)
	for name, raw := range c.Blocks {
		out[name] = string(raw)
	}
	return out
}

func walkLeaves(v reflect.Value, prefix string, out map[string]any) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name := yamlName(t.Field(i))
		if name == "" || name == "-" {
			continue
		}
		fv := v.Field(i)
		path := joinPath(prefix, name)
		if fv.Kind() == reflect.Struct {
			walkLeaves(fv, path, out)
			continue
		}
		out[path] = fv.Interface()
	}
}
