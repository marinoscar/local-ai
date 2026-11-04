import { describe, expect, it } from 'vitest';

import { isAllowedVpsHost, isPublicIpv4, isTailnetAddress, vpsHostFor, VpsInputError, VPS_HOST_RE } from './vps-host.js';

const ip = (...parts: number[]) => parts.join('.');

describe('the host regex', () => {
  it.each([
    ['ai-n1.marin.cr', true],
    ['ai-n3.marin.cr', true],
    ['ai-n9999.marin.cr', true],
    ['ai-n3-staging.marin.cr', true],
    ['ai-n3-a1b2c3d4e5f6g7h8.marin.cr', true], // 16 characters, the longest suffix
    ['ai-n3-a1b2c3d4e5f6g7h8i.marin.cr', false], // 17
    ['ai-n0.marin.cr', false],
    ['ai-n03.marin.cr', false],
    ['ai-n10000.marin.cr', false],
    ['ai-n3-.marin.cr', false],
    ['ai-n3-STAGING.marin.cr', false],
    ['ai-n3_staging.marin.cr', false],
    ['ai-n3--x.marin.cr', false],
    ['ai-n3-a-b.marin.cr', false], // the suffix is letters and digits only
    ['ai-n3.marin.cr.evil.test', false],
    ['xai-n3.marin.cr', false],
    ['ai-n3.marin.cr/../x', false],
    ['ai-n3.marin.cr\n', false],
    ['ai-n3.marin.cr;rm -rf', false],
    ['ai-n3.other.test', false],
    ['localhost', false],
    ['', false],
  ])('%j -> %s', (host, expected) => {
    expect(VPS_HOST_RE.test(host)).toBe(expected);
  });
});

describe('the staging suffix rule (the suffix is allowed only when it is the configured one)', () => {
  it.each([
    ['ai-n3.marin.cr', 'ai-n3', '', true],
    ['ai-n3-staging.marin.cr', 'ai-n3', '-staging', true],
    ['ai-n3-staging.marin.cr', 'ai-n3', '', false], // empty suffix: only ai-nN.marin.cr
    ['ai-n3.marin.cr', 'ai-n3', '-staging', false], // configured suffix, but the bare host
    ['ai-n3-other.marin.cr', 'ai-n3', '-staging', false], // any other suffix is refused
    ['ai-n4-staging.marin.cr', 'ai-n3', '-staging', false], // another node's host
    ['ai-n3-staging.marin.cr', 'ai-n3', '-STAGING', false], // an invalid configured suffix
    ['ai-n3-staging.marin.cr', 'ai-n3', 'staging', false],
    ['ai-n3-staging.marin.cr', 'bad', '-staging', false],
  ])('host %s, node %s, suffix %j -> %s', (host, nodeId, suffix, expected) => {
    expect(isAllowedVpsHost(host, nodeId, suffix)).toBe(expected);
  });

  it('builds the host and refuses bad input with a fixed message', () => {
    expect(vpsHostFor('ai-n3', '-staging')).toBe('ai-n3-staging.marin.cr');
    expect(vpsHostFor('ai-n12', '')).toBe('ai-n12.marin.cr');
    expect(() => vpsHostFor('ai-n3', '; x')).toThrow(VpsInputError);
    expect(() => vpsHostFor('../etc', '')).toThrow(/lab node id/);
    try {
      vpsHostFor('ai-n3', '$(whoami)');
    } catch (error) {
      expect((error as Error).message).not.toContain('whoami');
    }
  });
});

describe('address checks', () => {
  it.each([
    [ip(100, 64, 0, 1), true],
    [ip(100, 127, 255, 254), true],
    [ip(100, 63, 0, 1), false],
    [ip(100, 128, 0, 1), false],
    ['fd7a:115c:a1e0:ab12:4843:cd96:6262:1234', true], // tailnet IPv6 prefix, up to five more groups
    ['fd7a:115c:a1e0:ab12:4843:cd96:6262:1234:1', false],
    ['fd7a:115c:a1e0:1::1', false],
    ['10.0.0.1', false],
  ])('tailnet address %s -> %s', (value, expected) => expect(isTailnetAddress(value)).toBe(expected));

  it.each([
    [['203', '0', '113', '10'].join('.'), true],
    ['8.8.8.8', true],
    ['10.1.2.3', false],
    ['172.16.0.1', false],
    ['172.32.0.1', true],
    [ip(192, 168, 1, 1), false],
    [ip(100, 100, 1, 1), false],
    ['127.0.0.1', false],
    ['169.254.1.1', false],
    ['0.0.0.0', false],
    ['224.0.0.1', false],
    ['999.1.1.1', false],
    ['not an ip', false],
  ])('public IPv4 %s -> %s', (value, expected) => expect(isPublicIpv4(value)).toBe(expected));
});
