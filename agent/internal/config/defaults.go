package config

import "path/filepath"

// NodeYAMLPath is /etc/localai/node.yaml under root ("" means the real root).
func NodeYAMLPath(root string) string {
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, "etc", "localai", "node.yaml")
}

// UnconfiguredNodeID is the node id of the safe defaults.
const UnconfiguredNodeID = "unconfigured"

// Canonical ports (overview 3.4).
const (
	DefaultAgentPort   = 7070
	DefaultLlamaPort   = 8080
	DefaultKiwixPort   = 8888
	DefaultWhisperPort = 8081
)

// DefaultCloudURL is the cloud app's address when node.yaml does not set cloud.url.
const DefaultCloudURL = "https://ai.marin.cr"

// CloudJobNames returns the 14 job names cloud.allow accepts (agent.md 4.8.6), names without the
// "localai." prefix. It returns a fresh slice each call.
func CloudJobNames() []string {
	return []string{
		"status.refresh", "model.load", "model.unload", "model.set_default", "model.download",
		"model.delete", "ark.sync", "ark.verify", "mode.offline", "key.rotate", "service.restart",
		"logs.tail", "catalog.refresh", "agent.update",
	}
}

// Defaults returns the values an absent field takes (agent.md 3.4). The required fields
// (schema_version, node.id, profile) are left empty: their absence is a validation error.
// llama.backend and llama.commit stay empty here; Hooks.DetectBackend and Hooks.BuildCommit fill them.
func Defaults() Config {
	return Config{
		Catalog: CatalogBlock{
			Channel: "stable",
			BaseURL: "https://raw.githubusercontent.com/marinoscar/local-ai",
		},
		Llama: LlamaBlock{
			Mode:               "router",
			Build:              "/opt/localai/llama.cpp/current",
			LoadTimeoutSeconds: 300,
			LegacyAliases:      map[string][]string{},
			ExtraEnv:           map[string]string{},
		},
		Tailscale: TailscaleBlock{Manage: false, ServePattern: "none"},
		Cloud: CloudBlock{
			URL:         DefaultCloudURL,
			Allow:       CloudJobNames(),
			Concurrency: 1,
			Poll:        PollBlock{HeartbeatSeconds: 15, ClaimSeconds: 3, StateSeconds: 60},
		},
		Update:  UpdateBlock{Repo: "marinoscar/local-ai", Channel: "stable"},
		UI:      UIBlock{Language: "auto"},
		Ports:   PortsBlock{Agent: DefaultAgentPort, Llama: DefaultLlamaPort, Kiwix: DefaultKiwixPort, Whisper: DefaultWhisperPort},
		Logging: LoggingBlock{Level: "info"},
	}
}

// SafeDefaults is the config the agent runs on when node.yaml is missing (LAI-AGT-001) or invalid
// at start (LAI-AGT-002): no cloud, tailscale.manage false, canonical ports, node.id "unconfigured".
// Survival and the Portal stay reachable on it.
func SafeDefaults() Config {
	c := Defaults()
	c.SchemaVersion = SchemaVersion
	c.Node.ID = UnconfiguredNodeID
	c.Node.DisplayName = UnconfiguredNodeID
	c.Cloud.URL = ""
	c.Cloud.Allow = []string{}
	c.Tailscale = TailscaleBlock{Manage: false, ServePattern: "none"}
	c.SafeDefaults = true
	return c
}
