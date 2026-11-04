// Renders the commands and files the publish checklist shows (docs/specs/cloud-app.md section 4.8, runbook 05 part 7).
//
// The templates in `templates/` hold placeholders only. THIS is the one place a node's real tailnet address and name
// meet them, and only the lab:admin checklist route calls it, so the real values exist in that response and nowhere
// else: not in a log line, not in an audit row, not in a stored `detail`. A value the node row does not have yet is
// left as the same placeholder the runbook uses (`<TAILNET_IP_AI_N4>`), and `missing` says so.
import type { ChecklistStep } from './wizard.schemas';
import { tailnetIpPlaceholder } from './node-id';
import { BOOTSTRAP_HTTP_TEMPLATE } from './templates/bootstrap-http.conf';
import { PATTERN_A_TEMPLATE } from './templates/pattern-a.conf';
import { PATTERN_B_TEMPLATE } from './templates/pattern-b.conf';

export interface SnippetNode {
  nodeId: string;
  publicHost: string;
  servePattern: 'A' | 'B' | 'none';
  /** The machine name (`dell-linux`) or its full MagicDNS name. */
  tailnetMachine: string | null;
  tailnetIp: string | null;
}

export interface Snippet {
  title: string;
  language: 'bash' | 'nginx' | 'text';
  /** Where the content goes on the VPS, for a file. */
  path?: string;
  content: string;
}

export type MissingValue = 'tailnetIp' | 'tailnetName';

export interface RenderedSnippets {
  snippets: Snippet[];
  /** Values the node row does not hold, so the snippet still shows their placeholder. */
  missing: MissingValue[];
}

/** Fills `{{name}}` in a template. A name with no value is a bug in the caller, not something to hide. */
export function renderTemplate(template: string, values: Readonly<Record<string, string>>): string {
  return template.replace(/\{\{([A-Za-z0-9]+)\}\}/g, (_match, name: string) => {
    const value = values[name];
    if (value === undefined) throw new Error(`template variable ${name} has no value`);
    return value;
  });
}

/** The node's MagicDNS name, or null when only a short machine name is stored (the tailnet's own name is not known). */
export function tailnetFqdn(machine: string | null): string | null {
  return machine !== null && machine.includes('.') ? machine : null;
}

function templateValues(node: SnippetNode): { values: Record<string, string>; missing: MissingValue[] } {
  const missing: MissingValue[] = [];
  const fqdn = tailnetFqdn(node.tailnetMachine);
  if (node.tailnetIp === null) missing.push('tailnetIp');
  if (fqdn === null) missing.push('tailnetName');
  const machine = node.tailnetMachine?.split('.')[0] ?? '<MACHINE>';
  return {
    values: {
      nodeId: node.nodeId,
      publicHost: node.publicHost,
      tailnetIp: node.tailnetIp ?? tailnetIpPlaceholder(node.nodeId),
      tailnetName: fqdn ?? `${machine}.<TAILNET>.ts.net`,
      machine,
    },
    missing,
  };
}

const vhostPath = (host: string) => `/opt/infra/proxy/nginx/conf.d/${host}.conf`;

/** Renders the snippets of one checklist step. Steps with nothing to show (the automatic ones) return an empty list. */
export function renderStepSnippets(step: ChecklistStep, node: SnippetNode, vpsHostname: string): RenderedSnippets {
  const { values, missing } = templateValues(node);
  const host = node.publicHost;
  const snippets: Snippet[] = [];
  let used: MissingValue[] = [];

  switch (step) {
    case 'tailnet': {
      const serve =
        node.servePattern === 'A'
          ? 'sudo tailscale serve --bg --tcp=18080 tcp://127.0.0.1:8080'
          : 'sudo tailscale serve --bg --https=443 http://127.0.0.1:8080';
      snippets.push({
        title: 'On the node: join the tailnet and expose llama inside it (never `funnel`)',
        language: 'bash',
        content: ['sudo tailscale up', serve, 'tailscale serve status'].join('\n'),
      });
      const reach =
        node.servePattern === 'A'
          ? [`tailscale ping ${values.machine}`, `curl -i http://${values.tailnetIp}:18080/health`]
          : [`tailscale ping ${values.machine}`, `curl -i --resolve ${values.tailnetName}:443:${values.tailnetIp} https://${values.tailnetName}/health`];
      snippets.push({ title: 'On the VPS: check it can reach the node', language: 'bash', content: reach.join('\n') });
      used = missing;
      break;
    }
    case 'dns':
      snippets.push({
        title: 'Route 53 record (zone marin.cr)',
        language: 'text',
        content: [`Name:   ${host}`, 'Type:   A', 'Value:  <VPS_PUBLIC_IP>', 'TTL:    300', 'Routing: simple', `(the same address as ${vpsHostname})`].join('\n'),
      });
      snippets.push({ title: 'Check from any computer', language: 'bash', content: `dig +short ${host}\ndig +short ${vpsHostname}` });
      break;
    case 'bootstrap_vhost':
      snippets.push({
        title: 'Bootstrap HTTP vhost (lets Let’s Encrypt check the name)',
        language: 'nginx',
        path: vhostPath(host),
        content: renderTemplate(BOOTSTRAP_HTTP_TEMPLATE, values),
      });
      snippets.push({
        title: 'On the VPS, as root: test, reload and check the challenge path',
        language: 'bash',
        content: [
          'docker exec proxy-nginx nginx -t',
          'docker exec proxy-nginx nginx -s reload',
          `echo "${node.nodeId}-ready" > /opt/infra/proxy/webroot/.well-known/acme-challenge/${node.nodeId}-test`,
          `curl -fsS http://${host}/.well-known/acme-challenge/${node.nodeId}-test`,
          `rm /opt/infra/proxy/webroot/.well-known/acme-challenge/${node.nodeId}-test`,
        ].join('\n'),
      });
      break;
    case 'certificate':
      snippets.push({
        title: 'On the VPS, as root: issue the certificate',
        language: 'bash',
        content: [
          'docker run --rm \\',
          '  -v /opt/infra/proxy/letsencrypt:/etc/letsencrypt \\',
          '  -v /opt/infra/proxy/webroot:/var/www/certbot \\',
          '  certbot/certbot:latest certonly \\',
          '  --webroot -w /var/www/certbot \\',
          `  -d ${host} \\`,
          '  --email <ADMIN_EMAIL> \\',
          '  --agree-tos \\',
          '  --no-eff-email',
          `ls -l /opt/infra/proxy/letsencrypt/live/${host}/`,
        ].join('\n'),
      });
      break;
    case 'https_vhost': {
      if (node.servePattern === 'none') {
        snippets.push({
          title: 'This node is not served on the tailnet',
          language: 'text',
          content: 'Its serve pattern is "none". Set Pattern A or B in the node’s Settings first, then come back for the vhost file.',
        });
        break;
      }
      snippets.push({
        title: `Final HTTPS vhost (Pattern ${node.servePattern})`,
        language: 'nginx',
        path: vhostPath(host),
        content: renderTemplate(node.servePattern === 'A' ? PATTERN_A_TEMPLATE : PATTERN_B_TEMPLATE, values),
      });
      snippets.push({
        title: 'On the VPS, as root: check and reload',
        language: 'bash',
        content: [`grep -n "ai-n\\|proxy_pass\\|proxy_ssl_name\\|ts.net" ${vhostPath(host)}`, 'docker exec proxy-nginx nginx -t', 'docker exec proxy-nginx nginx -s reload'].join('\n'),
      });
      used = node.servePattern === 'A' ? missing.filter((m) => m === 'tailnetIp') : missing;
      break;
    }
    case 'gateway_test':
      snippets.push({
        title: 'By hand, from any computer (the Check button does the same from the cloud)',
        language: 'bash',
        content: [
          `curl -i https://${host}/health`,
          `curl -s -o /dev/null -w '%{http_code}\\n' https://${host}/v1/models`,
          'read -rsp "Node API key: " LLAMA_API_KEY; echo',
          `curl -s -o /dev/null -w '%{http_code}\\n' https://${host}/v1/models -H "Authorization: Bearer $LLAMA_API_KEY"`,
          'unset LLAMA_API_KEY',
        ].join('\n'),
      });
      break;
    case 'enrolled':
    case 'key':
      break;
  }
  return { snippets, missing: used };
}
