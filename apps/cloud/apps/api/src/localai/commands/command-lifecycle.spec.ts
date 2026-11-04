import type { LocalaiCommandStatus } from '@prisma/client';

import {
  canRenew,
  canTransition,
  hardCapDeadline,
  isHeld,
  leaseExpiryAt,
  renewedLeaseExpiry,
  TERMINAL_STATUSES,
  type LeaseFields,
} from './command-lifecycle';

const NODE = 'node-1';
const TOKEN = 'token-1';
const T0 = new Date('2026-11-02T15:00:00.000Z');
const at = (ms: number) => new Date(T0.getTime() + ms);

function running(overrides: Partial<LeaseFields> = {}): LeaseFields {
  return { status: 'running', type: 'localai.model.load', nodeId: NODE, claimToken: TOKEN, claimedAt: T0, leaseExpiresAt: at(60_000), ...overrides };
}

describe('the command state machine', () => {
  const all: LocalaiCommandStatus[] = ['queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled'];

  it('allows exactly queued->running|expired|cancelled and running->succeeded|failed', () => {
    const allowed = new Set(['queued>running', 'queued>expired', 'queued>cancelled', 'running>succeeded', 'running>failed']);
    for (const from of all) for (const to of all) expect(canTransition(from, to)).toBe(allowed.has(`${from}>${to}`));
  });

  it('never leaves a terminal state, and running never goes back to queued (at most once)', () => {
    for (const from of TERMINAL_STATUSES) for (const to of all) expect(canTransition(from, to)).toBe(false);
    expect(canTransition('running', 'queued')).toBe(false);
  });
});

describe('lease and hard-cap math (fake clock)', () => {
  it('grants the type\'s lease from now', () => {
    expect(leaseExpiryAt('localai.status.refresh', T0)).toEqual(at(30_000));
    expect(leaseExpiryAt('localai.model.load', T0)).toEqual(at(60_000));
    expect(leaseExpiryAt('localai.agent.update', T0)).toEqual(at(300_000));
  });

  it('computes the hard cap from claimed_at: 300 s, 600 s for model.load, 900 s for agent.update', () => {
    expect(hardCapDeadline('localai.status.refresh', T0)).toEqual(at(300_000));
    expect(hardCapDeadline('localai.model.load', T0)).toEqual(at(600_000));
    expect(hardCapDeadline('localai.agent.update', T0)).toEqual(at(900_000));
  });

  it('a renewal extends the lease to now + lease while the cap is far off', () => {
    expect(renewedLeaseExpiry('localai.model.load', T0, at(20_000))).toEqual(at(80_000));
    expect(renewedLeaseExpiry('localai.model.load', T0, at(540_000))).toEqual(at(600_000));
  });

  it('clamps a late renewal\'s lease to the hard cap, and refuses one at or after it', () => {
    expect(renewedLeaseExpiry('localai.model.load', T0, at(580_000))).toEqual(at(600_000));
    expect(renewedLeaseExpiry('localai.model.load', T0, at(599_999))).toEqual(at(600_000));
    expect(renewedLeaseExpiry('localai.model.load', T0, at(600_000))).toBeNull();
    expect(renewedLeaseExpiry('localai.model.load', T0, at(700_000))).toBeNull();
    expect(renewedLeaseExpiry('localai.agent.update', T0, at(899_999))).not.toBeNull();
    expect(renewedLeaseExpiry('localai.agent.update', T0, at(900_000))).toBeNull();
    expect(renewedLeaseExpiry('localai.status.refresh', T0, at(300_000))).toBeNull();
  });

  it('keeps a command alive for exactly its hard cap by renewing on the advertised interval', () => {
    // model.load renews every 20 s: the lease never lapses and the last allowed renewal lands just before 600 s.
    let lease = leaseExpiryAt('localai.model.load', T0);
    let now = T0;
    let renewals = 0;
    for (;;) {
      now = new Date(now.getTime() + 20_000);
      const next = renewedLeaseExpiry('localai.model.load', T0, now);
      if (!next) break;
      expect(now.getTime()).toBeLessThan(lease.getTime()); // the previous lease was still alive when it was renewed
      lease = next;
      renewals += 1;
    }
    expect(renewals).toBe(29); // renewals at 20 s .. 580 s; the one at 600 s hits the cap
    expect(lease).toEqual(at(600_000));
  });
});

describe('the held-claim predicate', () => {
  it('holds for a running command of this node with this token and an unexpired lease', () => {
    expect(isHeld(running(), NODE, TOKEN, at(1))).toBe(true);
  });

  it('does not hold: wrong token, wrong node, not running, no lease, lease passed (also exactly at expiry)', () => {
    expect(isHeld(running(), NODE, 'other', at(1))).toBe(false);
    expect(isHeld(running(), 'node-2', TOKEN, at(1))).toBe(false);
    for (const status of ['queued', 'succeeded', 'failed', 'expired', 'cancelled'] as const) {
      expect(isHeld(running({ status }), NODE, TOKEN, at(1))).toBe(false);
    }
    expect(isHeld(running({ leaseExpiresAt: null }), NODE, TOKEN, at(1))).toBe(false);
    expect(isHeld(running(), NODE, TOKEN, at(60_000))).toBe(false);
    expect(isHeld(running(), NODE, TOKEN, at(60_001))).toBe(false);
    expect(isHeld(running({ claimToken: null }), NODE, null as never, at(1))).toBe(false);
  });

  it('canRenew adds the hard cap, per type', () => {
    expect(canRenew(running({ leaseExpiresAt: at(700_000) }), NODE, TOKEN, at(599_000))).toBe(true);
    expect(canRenew(running({ leaseExpiresAt: at(700_000) }), NODE, TOKEN, at(600_000))).toBe(false);
    const refresh = running({ type: 'localai.status.refresh', leaseExpiresAt: at(400_000) });
    expect(canRenew(refresh, NODE, TOKEN, at(299_999))).toBe(true);
    expect(canRenew(refresh, NODE, TOKEN, at(300_000))).toBe(false);
    expect(canRenew(running({ claimedAt: null }), NODE, TOKEN, at(1))).toBe(false);
  });
});
