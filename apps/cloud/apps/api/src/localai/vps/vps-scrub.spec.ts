import { randomBytes } from 'node:crypto';

import { scrubVpsText } from './vps-scrub';

const ip = (...parts: number[]) => parts.join('.');

describe('scrubVpsText', () => {
  it('removes AWS keys, every address, tailnet names and tokens; keeps ordinary sentences', () => {
    const keyId = `AKIA${randomBytes(8).toString('hex').toUpperCase()}`;
    const secret = randomBytes(30).toString('base64').replace(/[^A-Za-z0-9]/g, 'x').slice(0, 40);
    const tailnet = ['tail', '1a2b3c'].join('');
    const text = `${keyId} ${secret} ${ip(100, 90, 1, 2)} ${ip(203, 0, 113, 10)} dell-linux.${tailnet}.ts.net nod_${'a'.repeat(24)}`;
    const out = scrubVpsText(text);
    for (const leak of [keyId, secret, ip(100, 90, 1, 2), ip(203, 0, 113, 10), tailnet, 'nod_aaaa']) expect(out).not.toContain(leak);
    expect(scrubVpsText('nginx -t passed for ai-n3-staging.marin.cr')).toBe('nginx -t passed for ai-n3-staging.marin.cr');
  });

  it('caps the length and flattens lines', () => {
    const out = scrubVpsText(`a\nb${'c'.repeat(900)}`, 100);
    expect(out).toHaveLength(100);
    expect(out).not.toContain('\n');
  });
});
