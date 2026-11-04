import { randomBytes } from 'node:crypto';

import { describe, expect, it } from 'vitest';

import { scrubDetail } from './vps-scrub.js';

describe('scrubDetail', () => {
  it('removes AWS keys, addresses and tailnet names, and keeps ordinary sentences', () => {
    const keyId = `AKIA${randomBytes(8).toString('hex').toUpperCase()}`;
    const secret = randomBytes(30).toString('base64').replace(/[^A-Za-z0-9]/g, 'x').slice(0, 40);
    const tailnet = ['tail', '1a2b3c'].join('');
    const ip = ['100', '101', '2', '3'].join('.');
    const text = `key ${keyId} secret ${secret} node ${ip} name dell-linux.${tailnet}.ts.net and the vps ${['203', '0', '113', '10'].join('.')}`;
    const out = scrubDetail(text);
    for (const leak of [keyId, secret, ip, tailnet, ['203', '0', '113', '10'].join('.')]) expect(out).not.toContain(leak);
    expect(scrubDetail('nginx -t passed and the proxy was reloaded.')).toBe('nginx -t passed and the proxy was reloaded.');
  });

  it('caps the length and flattens newlines', () => {
    const out = scrubDetail(`a\n${'b'.repeat(900)}`, 500);
    expect(out.length).toBe(500);
    expect(out).not.toContain('\n');
  });

  it('keeps a lab host name', () => {
    expect(scrubDetail('Published ai-n3-staging.marin.cr')).toBe('Published ai-n3-staging.marin.cr');
  });
});
