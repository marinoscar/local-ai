// The templates keep placeholders; the checklist route fills in the node's real values. The golden files in
// test/fixtures/localai/wizard/ are the templates rendered for a node with NO tailnet address stored, so they hold the
// placeholders the runbook uses and prove that nothing real is in the committed text. UPDATE_GOLDEN=1 rewrites them.
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

import { BOOTSTRAP_HTTP_TEMPLATE } from './templates/bootstrap-http.conf';
import { PATTERN_A_TEMPLATE } from './templates/pattern-a.conf';
import { PATTERN_B_TEMPLATE } from './templates/pattern-b.conf';
import { renderStepSnippets, renderTemplate, tailnetFqdn, type SnippetNode } from './snippets';
import { CHECKLIST_STEPS } from './wizard.schemas';

// A tailnet name, built from parts so no literal one sits in the repository.
const TAILNET = ['tail', '1a2b3c'].join('');
const GOLDEN_DIR = join(__dirname, '../../../test/fixtures/localai/wizard');
const placeholderNode = (servePattern: 'A' | 'B'): SnippetNode => ({ nodeId: 'ai-n4', publicHost: 'ai-n4.marin.cr', servePattern, tailnetMachine: 'dell-linux', tailnetIp: null });

function golden(name: string, actual: string) {
  const path = join(GOLDEN_DIR, name);
  if (process.env.UPDATE_GOLDEN === '1' || !existsSync(path)) {
    mkdirSync(GOLDEN_DIR, { recursive: true });
    writeFileSync(path, actual);
  }
  expect(actual).toBe(readFileSync(path, 'utf8'));
}

const vhost = (node: SnippetNode) => renderStepSnippets('https_vhost', node, 'ai.marin.cr').snippets.find((snippet) => snippet.language === 'nginx')!;

describe('golden vhost files (placeholders only)', () => {
  it('Pattern A', () => golden('pattern-a.conf', vhost(placeholderNode('A')).content));
  it('Pattern B', () => golden('pattern-b.conf', vhost(placeholderNode('B')).content));
  it('bootstrap HTTP vhost', () => golden('bootstrap-http.conf', renderStepSnippets('bootstrap_vhost', placeholderNode('B'), 'ai.marin.cr').snippets[0]!.content));

  it('the committed templates and goldens hold no address, only {{variables}} and <PLACEHOLDERS>', () => {
    const ipv4 = /\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b/;
    for (const text of [BOOTSTRAP_HTTP_TEMPLATE, PATTERN_A_TEMPLATE, PATTERN_B_TEMPLATE, vhost(placeholderNode('A')).content, vhost(placeholderNode('B')).content]) {
      // Loopback is the node-local llama address in the comments; every other address would be a leak.
      expect(text.replaceAll('127.0.0.1', '')).not.toMatch(ipv4);
      expect(text).not.toMatch(/\btail[0-9a-f]{6}\b/); // a tailnet name
    }
    expect(vhost(placeholderNode('A')).content).toContain('<TAILNET_IP_AI_N4>');
    expect(vhost(placeholderNode('B')).content).toContain('dell-linux.<TAILNET>.ts.net');
  });
});

describe('rendering with the node row', () => {
  // Built from parts: no literal tailnet address in the repository.
  const ip = ['100', '71', '8', '9'].join('.');
  const real: SnippetNode = { ...placeholderNode('B'), tailnetMachine: `dell-linux.${TAILNET}.ts.net`, tailnetIp: ip };

  it('fills the real tailnet address and name into the final vhost', () => {
    const rendered = renderStepSnippets('https_vhost', real, 'ai.marin.cr');
    const file = rendered.snippets.find((snippet) => snippet.language === 'nginx')!;
    expect(file.content).toContain(`proxy_pass https://${ip}:443;`);
    expect(file.content).toContain(`proxy_ssl_name dell-linux.${TAILNET}.ts.net;`);
    expect(file.content).toContain(`proxy_set_header Host dell-linux.${TAILNET}.ts.net;`);
    expect(file.path).toBe('/opt/infra/proxy/nginx/conf.d/ai-n4.marin.cr.conf');
    expect(rendered.missing).toEqual([]);
  });

  it('uses the plain-HTTP upstream on 18080 for Pattern A, with no proxy_ssl lines', () => {
    const file = vhost({ ...real, servePattern: 'A' });
    expect(file.content).toContain(`proxy_pass http://${ip}:18080;`);
    expect(file.content).not.toContain('proxy_ssl');
  });

  it('keeps what the runbook says to keep', () => {
    for (const pattern of ['A', 'B'] as const) {
      const text = vhost({ ...real, servePattern: pattern }).content;
      for (const line of ['proxy_set_header Authorization $http_authorization;', 'proxy_buffering off;', 'proxy_cache off;', 'proxy_set_header Connection "";', 'client_max_body_size 100m;', 'proxy_connect_timeout 15s;', 'proxy_read_timeout 600s;', 'proxy_send_timeout 600s;']) {
        expect(text).toContain(line);
      }
      expect(text).toContain('ssl_certificate /etc/letsencrypt/live/ai-n4.marin.cr/fullchain.pem;');
    }
  });

  it('reports what the row lacks and leaves the placeholder in place', () => {
    const rendered = renderStepSnippets('https_vhost', { ...placeholderNode('B'), tailnetMachine: 'dell-linux' }, 'ai.marin.cr');
    expect(rendered.missing).toEqual(['tailnetIp', 'tailnetName']);
    expect(rendered.snippets[0]!.content).toContain('<TAILNET_IP_AI_N4>');
    expect(renderStepSnippets('https_vhost', { ...placeholderNode('A'), tailnetIp: ip }, 'ai.marin.cr').missing).toEqual([]);
  });

  it('a node that is not served on the tailnet gets an explanation, not a vhost', () => {
    const rendered = renderStepSnippets('https_vhost', { ...real, servePattern: 'none' }, 'ai.marin.cr');
    expect(rendered.snippets).toHaveLength(1);
    expect(rendered.snippets[0]!.language).toBe('text');
  });

  it('every step renders, the automatic ones with nothing to show', () => {
    for (const step of CHECKLIST_STEPS) {
      const rendered = renderStepSnippets(step, real, 'ai.marin.cr');
      expect(Array.isArray(rendered.snippets)).toBe(true);
    }
    expect(renderStepSnippets('enrolled', real, 'ai.marin.cr').snippets).toEqual([]);
    expect(renderStepSnippets('key', real, 'ai.marin.cr').snippets).toEqual([]);
  });

  it('shows the runbook commands: dig, certbot with the host, the key read without echo', () => {
    expect(renderStepSnippets('dns', real, 'ai.marin.cr').snippets.map((s) => s.content).join('\n')).toContain('dig +short ai-n4.marin.cr');
    expect(renderStepSnippets('certificate', real, 'ai.marin.cr').snippets[0]!.content).toContain('-d ai-n4.marin.cr');
    const gateway = renderStepSnippets('gateway_test', real, 'ai.marin.cr').snippets[0]!.content;
    expect(gateway).toContain('read -rsp "Node API key: " LLAMA_API_KEY');
    expect(gateway).toContain('unset LLAMA_API_KEY');
  });

  it('puts no email address in any snippet', () => {
    for (const step of CHECKLIST_STEPS) expect(JSON.stringify(renderStepSnippets(step, real, 'ai.marin.cr'))).not.toMatch(/[\w.]+@[\w.]+/);
  });
});

describe('renderTemplate and tailnetFqdn', () => {
  it('fills every {{name}}', () => expect(renderTemplate('a {{x}} b {{x}} {{y}}', { x: '1', y: '2' })).toBe('a 1 b 1 2'));
  it('throws for a name with no value rather than printing it', () => expect(() => renderTemplate('{{x}}', {})).toThrow('x'));
  it('a short machine name has no known tailnet name', () => {
    expect(tailnetFqdn('dell-linux')).toBeNull();
    expect(tailnetFqdn(null)).toBeNull();
    expect(tailnetFqdn(`dell-linux.${TAILNET}.ts.net`)).toBe(`dell-linux.${TAILNET}.ts.net`);
  });
});
