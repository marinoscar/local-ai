import { FailedAuthLimiter } from './failed-auth-limiter';

describe('FailedAuthLimiter', () => {
  let now = 1_000_000;
  const limiter = () => new FailedAuthLimiter(() => now);

  beforeEach(() => {
    now = 1_000_000;
  });

  it('lets the first 30 failures through and blocks from the 31st attempt', () => {
    const l = limiter();
    for (let i = 1; i <= 30; i += 1) {
      expect(l.blockedForSeconds('203.0.113.7')).toBeNull();
      l.recordFailure('203.0.113.7');
      now += 100;
    }
    expect(l.blockedForSeconds('203.0.113.7')).toBe(300);
  });

  it('lifts the block after 5 minutes (fake clock) and starts counting again', () => {
    const l = limiter();
    for (let i = 0; i < 30; i += 1) l.recordFailure('203.0.113.7');
    expect(l.blockedForSeconds('203.0.113.7')).toBe(300);
    now += 299_000;
    expect(l.blockedForSeconds('203.0.113.7')).toBe(1);
    now += 1_000;
    expect(l.blockedForSeconds('203.0.113.7')).toBeNull();
    for (let i = 0; i < 29; i += 1) l.recordFailure('203.0.113.7');
    expect(l.blockedForSeconds('203.0.113.7')).toBeNull();
  });

  it('counts failures inside a sliding one-minute window only', () => {
    const l = limiter();
    for (let i = 0; i < 29; i += 1) l.recordFailure('203.0.113.7');
    now += 61_000;
    l.recordFailure('203.0.113.7');
    expect(l.blockedForSeconds('203.0.113.7')).toBeNull();
  });

  it('tracks each address on its own', () => {
    const l = limiter();
    for (let i = 0; i < 30; i += 1) l.recordFailure('203.0.113.7');
    expect(l.blockedForSeconds('203.0.113.7')).not.toBeNull();
    expect(l.blockedForSeconds('203.0.113.8')).toBeNull();
  });
});
