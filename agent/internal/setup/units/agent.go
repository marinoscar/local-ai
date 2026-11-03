package units

import "embed"

// Agent is the localai-agent.service template (agent.md section 3.6, AGENT-20). It is the unit the
// drill service Requires, so scripts/verify-units.sh verifies the drill pair against this file
// instead of a stub.
//
//go:embed localai-agent.service
var Agent embed.FS

// AgentUnit is the file name of the agent unit inside Agent.
const AgentUnit = "localai-agent.service"

// AgentTemplate returns the bytes of the agent unit.
func AgentTemplate() ([]byte, error) { return Agent.ReadFile(AgentUnit) }
