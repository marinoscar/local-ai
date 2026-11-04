// Package templates holds the files `aiagent setup` writes verbatim (installer.md section 4.3).
package templates

import "embed"

// PolkitRuleName is the file name of the polkit rule, under /etc/polkit-1/rules.d/.
const PolkitRuleName = "50-localai-operators.rules"

//go:embed 50-localai-operators.rules
var files embed.FS

// PolkitRule returns the bytes of the rule that lets localai-operators start and restart
// localai-agent.service without the admin password, and nothing else (installer.md section 4.3 P5).
func PolkitRule() []byte {
	b, err := files.ReadFile(PolkitRuleName)
	if err != nil {
		panic("templates: embedded polkit rule missing: " + err.Error())
	}
	return b
}
