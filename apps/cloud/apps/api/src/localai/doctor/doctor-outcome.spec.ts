import { leakBait } from '../../../test/localai/support/doctor-asserts';
import { CLOUD_CODES } from './codes';
import { buildOutcome, nameList, scrubText, statusForSeverity } from './doctor-outcome';

describe('scrubText', () => {
  it('removes a 64-hex string, a node or lai token and an IPv4 address', () => {
    const bait = leakBait();
    const text = scrubText(`digest ${bait.hex64} token ${bait.token} lai_${'a'.repeat(20)} from ${bait.ip}`);
    expect(text).not.toContain(bait.hex64);
    expect(text).not.toContain(bait.token);
    expect(text).not.toContain(bait.ip);
    expect(text).not.toMatch(/lai_a/);
    expect(text).toContain('[redacted]');
  });

  it('keeps ordinary text and version numbers', () => {
    expect(scrubText('ai-n4 runs aictl 0.1.0 and catalog 2026.11.02-1')).toBe('ai-n4 runs aictl 0.1.0 and catalog 2026.11.02-1');
  });
});

describe('buildOutcome', () => {
  it('leads with the code title, carries the code in data and uses the fix as the remedy', () => {
    const outcome = buildOutcome({ status: 'warn', code: CLOUD_CODES.nodeNeverConnected, found: 'Retire or finish ai-n4', data: { count: 1 } });
    expect(outcome.detail).toBe('A node was added but never connected [LAI-CLD-031]: Retire or finish ai-n4');
    expect(outcome.remedy).toBe(CLOUD_CODES.nodeNeverConnected.fix);
    expect(outcome.data).toEqual({ count: 1, code: 'LAI-CLD-031' });
  });

  it('gives a pass no remedy, and scrubs every string it carries', () => {
    const bait = leakBait();
    const pass = buildOutcome({ status: 'pass', detail: `ok ${bait.ip}`, data: { note: bait.hex64 } });
    expect(pass.remedy).toBeUndefined();
    expect(JSON.stringify(pass)).not.toContain(bait.ip);
    expect(JSON.stringify(pass)).not.toContain(bait.hex64);
    const fail = buildOutcome({ status: 'fail', detail: 'x', remedy: 'y', error: `boom ${bait.token}` });
    expect(fail.error).not.toContain(bait.token);
  });

  it('maps warning to warn, and error and critical to fail', () => {
    expect(statusForSeverity('warning')).toBe('warn');
    expect(statusForSeverity('error')).toBe('fail');
    expect(statusForSeverity('critical')).toBe('fail');
  });

  it('caps a name list', () => {
    expect(nameList(['ai-n1', 'ai-n2'])).toBe('ai-n1, ai-n2');
    expect(nameList(['a', 'b', 'c', 'd', 'e', 'f', 'g'])).toBe('a, b, c, d, e and 2 more');
  });
});
