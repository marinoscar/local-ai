import { isAllowedVpsHost, vpsHostFor, VPS_HOST_RE } from './vps-host';

describe('the host regex', () => {
  it.each([
    ['ai-n1.marin.cr', true],
    ['ai-n9999.marin.cr', true],
    ['ai-n3-staging.marin.cr', true],
    ['ai-n3-a1b2c3d4e5f6g7h8.marin.cr', true],
    ['ai-n3-a1b2c3d4e5f6g7h8i.marin.cr', false],
    ['ai-n0.marin.cr', false],
    ['ai-n03.marin.cr', false],
    ['ai-n10000.marin.cr', false],
    ['ai-n3-.marin.cr', false],
    ['ai-n3--x.marin.cr', false],
    ['ai-n3-STAGING.marin.cr', false],
    ['ai-n3.marin.cr.evil.test', false],
    ['xai-n3.marin.cr', false],
    ['ai-n3.marin.cr\n', false],
    ['ai-n3.other.test', false],
    ['', false],
  ])('%j -> %s', (host, expected) => expect(VPS_HOST_RE.test(host)).toBe(expected));
});

describe('the staging suffix rule', () => {
  it.each([
    ['ai-n3.marin.cr', 'ai-n3', '', true],
    ['ai-n3-staging.marin.cr', 'ai-n3', '-staging', true],
    ['ai-n3-staging.marin.cr', 'ai-n3', '', false], // empty suffix: only ai-nN.marin.cr
    ['ai-n3.marin.cr', 'ai-n3', '-staging', false],
    ['ai-n3-other.marin.cr', 'ai-n3', '-staging', false], // any other suffix is refused
    ['ai-n4-staging.marin.cr', 'ai-n3', '-staging', false],
    ['ai-n3-staging.marin.cr', 'ai-n3', '-STAGING', false],
    ['ai-n3-staging.marin.cr', 'ai-n3', 'staging', false],
    ['ai-n3-staging.marin.cr', 'x', '-staging', false],
  ])('host %s, node %s, suffix %j -> %s', (host, nodeId, suffix, expected) => expect(isAllowedVpsHost(host, nodeId, suffix)).toBe(expected));

  it('builds the host and refuses bad input', () => {
    expect(vpsHostFor('ai-n3', '-staging')).toBe('ai-n3-staging.marin.cr');
    expect(vpsHostFor('ai-n12', '')).toBe('ai-n12.marin.cr');
    expect(() => vpsHostFor('ai-n3', '; x')).toThrow();
    expect(() => vpsHostFor('../etc', '')).toThrow();
  });
});
