import { createHash } from 'node:crypto';

import {
  GATEWAY_TOKEN_HASH_PATTERN,
  GATEWAY_TOKEN_LENGTH,
  GATEWAY_TOKEN_PATTERN,
  gatewayTokenPrefix,
  generateGatewayToken,
  hashGatewayToken,
  isWellFormedGatewayToken,
  mintGatewayToken,
} from './gateway.token';

describe('generateGatewayToken', () => {
  it('is lai_ plus 43 base64url characters (47 in all)', () => {
    for (let i = 0; i < 200; i += 1) {
      const token = generateGatewayToken();
      expect(token).toMatch(/^lai_[A-Za-z0-9_-]{43}$/);
      expect(token).toHaveLength(GATEWAY_TOKEN_LENGTH);
      expect(token).toHaveLength(47);
    }
  });

  it('uses 32 random bytes with no base64 padding', () => {
    const sizes: number[] = [];
    const token = generateGatewayToken((size) => {
      sizes.push(size);
      return Buffer.alloc(size, 0xff);
    });
    expect(sizes).toEqual([32]);
    expect(token).toBe(`lai_${'_'.repeat(42)}8`); // 0xff * 32 in base64url, unpadded
    expect(token).not.toContain('=');
  });

  it('never repeats', () => {
    const seen = new Set(Array.from({ length: 500 }, () => generateGatewayToken()));
    expect(seen.size).toBe(500);
  });
});

describe('hashGatewayToken', () => {
  it('is the sha256 hex of the whole token, lai_ included', () => {
    const token = generateGatewayToken();
    const expected = createHash('sha256').update(token).digest('hex');
    expect(hashGatewayToken(token)).toBe(expected);
    expect(hashGatewayToken(token)).toMatch(GATEWAY_TOKEN_HASH_PATTERN);
    expect(hashGatewayToken(token)).not.toBe(createHash('sha256').update(token.slice(4)).digest('hex'));
  });
});

describe('gatewayTokenPrefix', () => {
  it('is lai_ plus the first 6 characters of the body (10 characters)', () => {
    const token = `lai_Ab3dEf${'x'.repeat(37)}`;
    expect(gatewayTokenPrefix(token)).toBe('lai_Ab3dEf');
    expect(gatewayTokenPrefix(token)).toHaveLength(10);
  });

  it('is too short to find a key by: it is a prefix, not the token', () => {
    const { token, tokenPrefix } = mintGatewayToken();
    expect(token.startsWith(tokenPrefix)).toBe(true);
    expect(token.length - tokenPrefix.length).toBe(37);
  });
});

describe('mintGatewayToken', () => {
  it('derives the hash and the prefix from the same token', () => {
    const minted = mintGatewayToken();
    expect(minted.tokenHash).toBe(hashGatewayToken(minted.token));
    expect(minted.tokenPrefix).toBe(gatewayTokenPrefix(minted.token));
    expect(minted.token).toMatch(GATEWAY_TOKEN_PATTERN);
  });
});

describe('isWellFormedGatewayToken', () => {
  const body = 'A'.repeat(43);
  it.each([
    [`lai_${body}`, true],
    [`lai_${'-_'.repeat(21)}-`, true],
    [`lai_${'A'.repeat(42)}`, false],
    [`lai_${'A'.repeat(44)}`, false],
    [`lat_${body}`, false],
    [`LAI_${body}`, false],
    [`lai_${'A'.repeat(42)}=`, false],
    [`lai_${'A'.repeat(42)}+`, false],
    [`lai_${body} `, false],
    ['', false],
    [undefined, false],
    [null, false],
    [123, false],
  ])('%j -> %s', (value, expected) => {
    expect(isWellFormedGatewayToken(value)).toBe(expected);
  });
});
