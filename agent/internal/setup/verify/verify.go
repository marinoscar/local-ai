// Package verify holds the installer's P11 serve-pattern check and the P15 post-install checks
// (docs/specs/installer.md section 4.3, `aiagent setup verify` and `aiagent setup serve-check`).
// Nothing here changes the machine; every check reads a file or asks the local agent.
package verify

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/setup/inventory"
	"gopkg.in/yaml.v3"
)

// Severity words printed in results (the registry's, lower case).
const (
	OK      = "ok"
	Info    = "info"
	Warning = "warning"
	Error   = "error"
)

// Result is one check outcome. Code is empty when the check passed.
type Result struct {
	Check    string
	Severity string
	Code     string
	Message  string
	Fix      string
}

// NodeYAML is the part of /etc/localai/node.yaml the installer checks read.
type NodeYAML struct {
	Node struct {
		ID string `yaml:"id"`
	} `yaml:"node"`
	Profile string `yaml:"profile"`
	Llama   struct {
		Backend string `yaml:"backend"`
	} `yaml:"llama"`
	Tailscale struct {
		ServePattern string `yaml:"serve_pattern"`
	} `yaml:"tailscale"`
}

// LoadNodeYAML reads node.yaml; a missing file gives an empty NodeYAML and no error.
func LoadNodeYAML(path string) (NodeYAML, error) {
	var n NodeYAML
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return n, nil
		}
		return n, err
	}
	if err := yaml.Unmarshal(data, &n); err != nil {
		return n, fmt.Errorf("%s is not valid YAML: %w", path, err)
	}
	return n, nil
}

func normPattern(p string) string {
	p = strings.ToUpper(strings.TrimSpace(p))
	if p == "NONE" {
		return ""
	}
	return p
}

func codeResult(check, code, sev, detail string) Result {
	title, fix, _ := doctor.Text(code, "en")
	msg := title
	if detail != "" {
		msg += ": " + detail
	}
	return Result{Check: check, Severity: sev, Code: code, Message: msg, Fix: fix}
}

// ServePattern decides the serve pattern to record (installer.md section 4.3 P11). Sources, strongest first:
// the --serve-pattern flag, node.yaml (tailscale.serve_pattern), the inventory entry of this node. When the
// inventory names a pattern and the flag or node.yaml names a different one, that is LAI-INS-050 and the
// returned Result has Severity Error (the installer stops, exit 1). With nothing known the pattern is "".
func ServePattern(nodeID, flagValue string, node NodeYAML, inv *inventory.Inventory) (string, *Result) {
	flagP, yamlP := normPattern(flagValue), normPattern(node.Tailscale.ServePattern)
	invP := ""
	if n := inv.Node(nodeID); n != nil {
		invP = normPattern(n.ServePattern)
	}
	if invP != "" {
		for _, c := range []struct{ from, v string }{{"--serve-pattern", flagP}, {"node.yaml", yamlP}} {
			if c.v != "" && c.v != invP {
				r := codeResult("serve-pattern", doctor.INS050, Error,
					fmt.Sprintf("%s says %s but the inventory says %s for %s", c.from, c.v, invP, nodeID))
				return c.v, &r
			}
		}
	}
	switch {
	case flagP != "":
		return flagP, nil
	case yamlP != "":
		return yamlP, nil
	}
	return invP, nil
}

// Env is what Run reads. Zero values get the canonical paths from overview.md section 3.2.
type Env struct {
	NodeID             string
	ServeFlag          string
	NodeYAMLPath       string
	InventoryPath      string
	PINPath            string
	RebootRequiredPath string
	HealthURL          string
	HTTP               *http.Client
}

// Defaults fills the canonical values.
func (e *Env) Defaults() {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	set(&e.NodeYAMLPath, "/etc/localai/node.yaml")
	set(&e.InventoryPath, inventory.DefaultPath)
	set(&e.PINPath, "/etc/localai/admin-pin")
	set(&e.RebootRequiredPath, "/var/run/reboot-required")
	set(&e.HealthURL, "http://127.0.0.1:7070/api/v1/health")
	if e.HTTP == nil {
		e.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
}

// Run executes the checks that can pass before a reboot and returns every result in a fixed order.
func Run(e Env) []Result {
	e.Defaults()
	var out []Result

	// 1. the agent answers
	status, herr := 0, error(nil)
	if resp, err := e.HTTP.Get(e.HealthURL); err != nil {
		herr = err
	} else {
		status = resp.StatusCode
		resp.Body.Close()
	}
	switch {
	case herr != nil:
		out = append(out, codeResult("agent", doctor.INS034, "critical", "no answer from "+e.HealthURL))
	case status != http.StatusOK:
		out = append(out, codeResult("agent", doctor.INS034, "critical", fmt.Sprintf("%s answered %d", e.HealthURL, status)))
	default:
		out = append(out, Result{Check: "agent", Severity: OK, Message: "the agent answers on " + e.HealthURL})
	}

	// 2. the serve pattern matches the inventory (an error here; doctor reports it as a warning later)
	node, err := LoadNodeYAML(e.NodeYAMLPath)
	inv, ierr := inventory.Load(e.InventoryPath)
	switch {
	case err != nil:
		out = append(out, Result{Check: "serve-pattern", Severity: Warning, Message: err.Error()})
	case ierr != nil && !errors.Is(ierr, inventory.ErrNotFound):
		out = append(out, Result{Check: "serve-pattern", Severity: Warning, Message: ierr.Error()})
	default:
		id := e.NodeID
		if id == "" {
			id = node.Node.ID
		}
		if p, bad := ServePattern(id, e.ServeFlag, node, inv); bad != nil {
			out = append(out, *bad)
		} else {
			msg := "serve pattern " + p
			if p == "" {
				msg = "no serve pattern recorded (the node is not published on the tailnet yet)"
			}
			out = append(out, Result{Check: "serve-pattern", Severity: OK, Message: msg})
		}
	}

	// 3. the admin PIN exists
	if _, err := os.Stat(e.PINPath); err != nil {
		out = append(out, codeResult("pin", doctor.INS060, Warning, ""))
	} else {
		out = append(out, Result{Check: "pin", Severity: OK, Message: "the admin PIN is set"})
	}

	// 4. a reboot is needed
	if _, err := os.Stat(e.RebootRequiredPath); err == nil {
		out = append(out, codeResult("reboot", doctor.INS070, Info, ""))
	}
	return out
}

// Failed reports whether any result stops the install (error or critical).
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Severity == Error || r.Severity == "critical" {
			return true
		}
	}
	return false
}

// Format renders one result as terminal text.
func (r Result) Format() string {
	if r.Code == "" {
		return fmt.Sprintf("  ok       %s: %s", r.Check, r.Message)
	}
	return fmt.Sprintf("  %-8s %s: %s\n           Fix: %s", strings.ToUpper(r.Severity), r.Code, r.Message, r.Fix)
}
