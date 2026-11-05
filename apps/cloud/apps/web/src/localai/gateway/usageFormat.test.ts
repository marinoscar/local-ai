import { barPercent, describeKey, describeNodeShares, describeRequests, describeTokens, describeTokenSplit } from './usageFormat';

describe('the usage rows', () => {
  it('writes requests and tokens like the wireframe', () => {
    expect(describeRequests(412)).toBe('412 requests');
    expect(describeRequests(1)).toBe('1 request');
    expect(describeRequests(12_345)).toBe('12,345 requests');
    expect(describeTokens({ promptTokens: 1_000_000, completionTokens: 200_000 })).toBe('1.2M tokens');
    expect(describeTokens({ promptTokens: 0, completionTokens: 1 })).toBe('1 token');
    expect(describeTokenSplit({ promptTokens: 1_000_000, completionTokens: 200_000 })).toBe('1M in · 200k out');
  });

  it('writes the node shares as rounded percentages of the requests that reached a node', () => {
    expect(
      describeNodeShares([
        { nodeId: 'ai-n1', requests: 251 },
        { nodeId: 'ai-n2', requests: 124 },
        { nodeId: 'ai-n3', requests: 37 },
      ]),
    ).toBe('ai-n1 61 % · ai-n2 30 % · ai-n3 9 %');
    expect(describeNodeShares([])).toBe('');
    expect(describeNodeShares(undefined)).toBe('');
    expect(describeNodeShares([{ nodeId: 'ai-n1', requests: 0 }])).toBe('');
  });

  it('labels a row with no model or node', () => {
    expect(describeKey('ai-n1')).toBe('ai-n1');
    expect(describeKey(null)).toBe('(none)');
  });

  it('scales the bar against the busiest row and keeps a sliver for any request', () => {
    expect(barPercent(412, 412)).toBe(100);
    expect(barPercent(206, 412)).toBe(50);
    expect(barPercent(1, 412)).toBe(2);
    expect(barPercent(0, 412)).toBe(0);
    expect(barPercent(5, 0)).toBe(0);
  });
});
