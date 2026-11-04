// Package summary renders the plain-language end-of-install summary with its TO DO list and the VPS
// publishing checklist (docs/specs/installer.md section 4.3 P15). The text is exact: the golden files in
// testdata/ are compared byte for byte.
//
// Real lab values (zone id, VPS address, tailnet name, node tailnet address, admin email) are filled in only
// when an inventory is given. The output goes to the local terminal and the local log, never to the cloud.
package summary

import (
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/setup/inventory"
)

// PublicDomain is the public lab domain (inventory.example.yaml lab.public_domain, safe to commit).
const PublicDomain = "marin.cr"

// Input is everything the summary shows. The caller (aiagent setup summary) fills it from state.json,
// node.yaml and the agent; this package does no I/O.
type Input struct {
	NodeID       string
	Profile      string
	Backend      string
	AgentVersion string
	Brain        string // model id of the brain
	APIKeyMasked string // ab12…ef90
	Content      string // "sync running (186 GB planned)   check: aictl ark sync", or why there is none
	Cloud        string // "enrolled at https://ai.marin.cr", or why not
	User         string // the desktop user who must log in again; empty: no such TO DO
	Machine      string // hostname -s
	Pattern      string // serve pattern recorded in P11: "A", "B" or "" (both are shown)

	Laptop         bool
	RebootRequired bool
	SkipTailscale  bool // --no-tailscale: no tailnet TO DO and no tailnet lines
	TailnetJoined  bool // tailscale status --json BackendState == Running
	CloudPublishes bool // phase 2: the cloud publishes the node, so there is no manual checklist
	Inventory      *inventory.Inventory
}

// placeholder builds <NAME_AI_N4> style tokens (the placeholders of inventory.example.yaml).
func placeholder(prefix, nodeID string) string {
	return "<" + prefix + "_" + strings.ToUpper(strings.ReplaceAll(nodeID, "-", "_")) + ">"
}

func or(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// Render returns the whole summary, ending with a newline.
func Render(in Input) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("Local AI node %s is installed.        (profile %s, backend %s, agent %s)", in.NodeID, in.Profile, in.Backend, in.AgentVersion)
	w("")
	w("  Brain:        %s", in.Brain)
	w("  Local UI:     http://127.0.0.1:7070          Survival Portal: http://127.0.0.1:7070/portal")
	w("  API key:      %s   (full key: aictl key show)", in.APIKeyMasked)
	w("  Content:      %s", in.Content)
	w("  Cloud:        %s", in.Cloud)
	w("")
	w("TO DO")
	if in.RebootRequired {
		w("  [ ] REBOOT this computer (laptop power settings apply after a reboot).   sudo reboot")
	}
	if in.User != "" {
		w("  [ ] Log out and back in, so %s can use the desktop app (group localai-operators).", in.User)
	}
	if !in.SkipTailscale && !in.TailnetJoined {
		w("  [ ] Join the tailnet:  sudo tailscale up --ssh=false")
	}
	if in.Laptop {
		w("  [ ] BIOS: turn on \"Power on after AC restore\".  Prefer an Ethernet cable over Wi-Fi.")
	}
	if !in.CloudPublishes {
		w("  [ ] Publish the node on the VPS (checklist below).")
	}

	if !in.SkipTailscale && !in.TailnetJoined {
		w("")
		w("Join the tailnet (do this once, then come back):")
		w("  sudo tailscale up --ssh=false")
		w("Open the URL it prints, sign in, and approve the machine \"%s\".", or(in.Machine, "<machine>"))
		w("Check:  tailscale status      (shows this machine and the VPS \"prod-server\")")
	}

	if in.Laptop {
		w("")
		w("LAPTOP LID TEST (after the reboot; details: docs/source/laptop-server-setup.md §5.1)")
		w("  1. Start the logger:")
		w("       nohup bash -c 'while true; do echo \"$(date +%%T) up=$(cut -d\" \" -f1 /proc/uptime) lid=$(awk \"{print \\$2}\" /proc/acpi/button/lid/*/state)\"; sleep 10; done' >> ~/lid-test.log 2>&1 &")
		w("  2. Close the lid for 5 to 10 minutes, open it, then read:  cat ~/lid-test.log")
		w("  3. Pass: lid=closed rows about 10 seconds apart, no gaps, and \"uptime -s\" has not changed.")
		w("  Wi-Fi can still stall; an Ethernet cable is strongly recommended.")
	}

	w("")
	if in.CloudPublishes {
		w("The cloud will publish %s.%s automatically.", in.NodeID, PublicDomain)
	} else {
		renderChecklist(&b, in)
	}
	return b.String()
}

func renderChecklist(b *strings.Builder, in Input) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format+"\n", a...) }
	inv := in.Inventory
	var lab inventory.Lab
	var vps inventory.VPS
	var node *inventory.Node
	if inv != nil {
		lab, vps, node = inv.Lab, inv.VPS, inv.Node(in.NodeID)
	}
	zone := or(lab.Route53ZoneID, "<ROUTE53_ZONE_ID>")
	vpsIP := or(vps.PublicIP, "<VPS_PUBLIC_IP>")
	tailnet := or(lab.Tailnet, "<TAILNET>")
	email := or(lab.OwnerEmail, "<ADMIN_EMAIL>")
	nodeIP := placeholder("TAILNET_IP", in.NodeID)
	if node != nil {
		nodeIP = or(node.TailnetIP, nodeIP)
	}
	machine := or(in.Machine, "<machine>")
	host := in.NodeID + "." + PublicDomain
	fqdn := machine + "." + tailnet + ".ts.net"

	w("VPS CHECKLIST for %s (run on the VPS as root; details: docs/source/vps-ai-nodes-setup.md §6)", in.NodeID)
	w("  1. On this node, check llama answers:   curl -s http://127.0.0.1:8080/health")
	w("  2. DNS: Route 53 zone %s (%s): A record %s -> %s, TTL 300", PublicDomain, zone, host, vpsIP)
	w("  3. From the VPS:  tailscale ping %s", machine)
	if in.Pattern != "A" {
		w("       Pattern B:  curl -i --resolve %s:443:%s https://%s/health", fqdn, nodeIP, fqdn)
	}
	if in.Pattern != "B" {
		w("       Pattern A:  curl -i http://%s:18080/health", nodeIP)
	}
	w("  4. Bootstrap HTTP vhost /opt/infra/proxy/nginx/conf.d/%s.conf (ACME webroot + redirect);", host)
	w("       docker exec proxy-nginx nginx -t && docker exec proxy-nginx nginx -s reload")
	w("  5. Certificate: certbot webroot for %s (email %s)", host, email)
	switch in.Pattern {
	case "A":
		w("  6. Final HTTPS vhost: copy ai-n1.marin.cr.conf (Pattern A); change names, cert paths,")
		w("       proxy_pass IP. Keep Authorization passthrough, buffering off, timeouts 15s/600s/600s.")
	case "B":
		w("  6. Final HTTPS vhost: copy ai-n2.marin.cr.conf (Pattern B); change names, cert paths,")
		w("       proxy_pass IP and proxy_ssl_name/Host. Keep Authorization passthrough, buffering off, timeouts 15s/600s/600s.")
	default:
		w("  6. Final HTTPS vhost: copy ai-n2.marin.cr.conf (Pattern B) or ai-n1.marin.cr.conf (Pattern A); change names, cert paths,")
		w("       proxy_pass IP and (B) proxy_ssl_name/Host. Keep Authorization passthrough, buffering off, timeouts 15s/600s/600s.")
	}
	w("  7. Validate: unauthenticated /v1/models -> 401; with the key -> 200.")
	w("  8. Commit the vhost in marin-server-infra; optionally add the node to Open WebUI.")
}
