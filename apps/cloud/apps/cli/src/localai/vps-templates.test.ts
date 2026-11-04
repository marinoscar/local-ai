// The CLI's copy of the vhost templates must equal the API's, rendered the way the API's golden files are.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import { renderBootstrapVhost, renderFinalVhost, renderTemplate } from './vps-templates.js';

const golden = (name: string) => readFileSync(fileURLToPath(new URL(`../../../api/test/fixtures/localai/wizard/${name}`, import.meta.url)), 'utf8');
const tailIp = ['100', '90', '1', '2'].join('.');
const tailName = `dell-linux.${['tail', 'abcdef'].join('')}.ts.net`;
const values = { nodeId: 'ai-n4', publicHost: 'ai-n4.marin.cr', tailnetIp: '<TAILNET_IP_AI_N4>', tailnetName: 'dell-linux.<TAILNET>.ts.net' };

describe('vhost templates', () => {
  it('Pattern A equals the wizard golden file', () => expect(renderFinalVhost('A', values)).toBe(golden('pattern-a.conf')));
  it('Pattern B equals the wizard golden file', () => expect(renderFinalVhost('B', values)).toBe(golden('pattern-b.conf')));
  it('the bootstrap vhost equals the wizard golden file', () => expect(renderBootstrapVhost(values)).toBe(golden('bootstrap-http.conf')));

  it('a missing variable is a bug, not a blank', () => {
    expect(() => renderTemplate('x {{nope}}', {})).toThrow(/nope/);
  });

  it('fills the node values and leaves nginx variables alone', () => {
    const text = renderFinalVhost('B', { ...values, tailnetIp: tailIp, tailnetName: tailName });
    expect(text).toContain(`proxy_pass https://${tailIp}:443;`);
    expect(text).toContain(`proxy_ssl_name ${tailName};`);
    expect(text).toContain('$http_authorization');
    expect(text).not.toContain('{{');
  });
});
