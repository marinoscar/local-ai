import { compactCount, describeExpiry, describeLimits, describeModels } from './format';

describe('the key table cells', () => {
  it('writes the limits like the wireframe', () => {
    expect(describeLimits({ rpm: 60, concurrent: 4, dailyTokens: 2_000_000 })).toBe('60 rpm · 4 · 2M/d');
    expect(describeLimits({ rpm: 600, concurrent: 16, dailyTokens: null })).toBe('600 rpm · 16 · ∞');
    expect(describeLimits({ rpm: 1, concurrent: 1, dailyTokens: 1500 })).toBe('1 rpm · 1 · 1.5k/d');
  });

  it('compacts counts', () => {
    expect([compactCount(999), compactCount(1000), compactCount(12_345), compactCount(2_500_000)]).toEqual(['999', '1k', '12.3k', '2.5M']);
  });

  it('says all for no restriction, else the first model and a count', () => {
    expect(describeModels([])).toBe('all');
    expect(describeModels(['a'])).toBe('a');
    expect(describeModels(['a', 'b', 'c'])).toBe('a +2');
  });

  it('shows the day of an expiry, or never', () => {
    expect(describeExpiry('2027-01-31T12:00:00.000Z')).toBe('2027-01-31');
    expect(describeExpiry(null)).toBe('never');
  });
});
