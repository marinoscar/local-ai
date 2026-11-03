package cloud

import (
	"reflect"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// Capabilities is the "capabilities" object of register and heartbeat; EAB stores it as it is and the
// cloud UI reads capabilities.localai (agent.md section 4.8.2).
type Capabilities struct {
	Localai LocalaiCapabilities `json:"localai"`
}

// LocalaiCapabilities is capabilities.localai. Allow tells the cloud UI which commands the node
// accepts (the LAI-CLD-057 hint); State is the reduced snapshot of the capabilities fallback and is
// present only while the cloud has no state endpoint.
type LocalaiCapabilities struct {
	Schema         int           `json:"schema"`
	AgentVersion   string        `json:"agent_version"`
	Backend        string        `json:"backend"`
	Profile        string        `json:"profile"`
	LlamaBuild     string        `json:"llama_build"`
	LlamaMode      string        `json:"llama_mode"`
	ModelsMax      int           `json:"models_max"`
	GPU            string        `json:"gpu,omitempty"`
	VRAMTotalMiB   int64         `json:"vram_total_mib"`
	RAMTotalMiB    int64         `json:"ram_total_mib"`
	CatalogVersion string        `json:"catalog_version,omitempty"`
	ServePattern   string        `json:"serve_pattern"`
	PublicHost     string        `json:"public_host,omitempty"`
	Allow          []string      `json:"allow"`
	State          *ReducedState `json:"state,omitempty"`
}

// statusBuildOptions are the options every cloud-bound status read uses: English, no counters.
var statusBuildOptions = statusapi.BuildOptions{Lang: "en"}

// BuildCapabilities fills capabilities.localai from the status view and node.yaml. Every string goes
// through the scrubber; a hit is dropped (LAI-CLD-058) and its path returned.
func BuildCapabilities(st statusapi.Status, cfg config.Config) (Capabilities, []string) {
	c := LocalaiCapabilities{
		Schema: 1, AgentVersion: cut(st.Node.AgentVersion, 64), Backend: oneOf(st.Node.Backend, "cpu", "cuda", "vulkan", "cpu"),
		Profile: cut(deref(st.Node.Profile), 64), LlamaBuild: cut(deref(st.Node.LlamaBuild), 80), LlamaMode: orStr(st.Node.LlamaMode, "router"),
		ModelsMax: clampInt(st.Models.ModelsMax, 1, 16), RAMTotalMiB: st.Hardware.RAMTotalMiB,
		ServePattern: oneOf(cfg.Tailscale.ServePattern, "none", "A", "B", "none"),
		Allow:        append([]string{}, cfg.Cloud.Allow...),
	}
	if st.Hardware.GPU != nil {
		c.GPU = cut(*st.Hardware.GPU, 128)
	}
	if st.Hardware.VRAMTotalMiB != nil {
		c.VRAMTotalMiB = *st.Hardware.VRAMTotalMiB
	}
	if st.Node.CatalogVersion != nil && ValidCatalogVersion(*st.Node.CatalogVersion) {
		c.CatalogVersion = *st.Node.CatalogVersion
	}
	if cfg.Node.PublicHost != nil {
		c.PublicHost = cut(*cfg.Node.PublicHost, 253)
	}
	caps := Capabilities{Localai: c}
	return caps, scrubValue(reflect.ValueOf(&caps), "capabilities")
}

// EligibleTypes returns "localai.<entry>" for each cloud.allow entry (agent.md section 4.8.2).
func EligibleTypes(allow []string) []string {
	out := make([]string, 0, len(allow))
	for _, a := range allow {
		out = append(out, "localai."+a)
	}
	return out
}
