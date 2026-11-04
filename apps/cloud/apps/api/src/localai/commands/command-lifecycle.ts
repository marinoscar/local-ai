// The command state machine and its time math, as PURE functions of a row and a clock (docs/specs/cloud-app.md
// section 4.4). The services call these to decide and to build the writes; the tests drive them with a fake clock.
//
//             enqueue                      claim (node long poll)
//   (none) ──────────► queued ──────────────────────────────► running ──result──► succeeded
//                        │  │ expires_at passed (sweeper)        │  │
//          cancel (UI)   │  └──► expired                         │  └─failure──► failed
//                        └─────► cancelled                       └─ lease passed (sweeper) ─► failed (LAI-CLD-059)
//
// "Held" is the one predicate that guards renew, result and failure: the command is `running`, belongs to this node,
// carries this claim token, and its lease has not passed. Every write that needs it repeats it in the `WHERE` of the
// UPDATE (never a read followed by a write), so a sweeper that failed the command a moment ago cannot be overtaken.
import type { LocalaiCommandStatus } from '@prisma/client';

import type { CommandType } from '../contracts/commands';
import { policyOf } from './command-policy';

/** The terminal statuses: nothing leaves them. */
export const TERMINAL_STATUSES = ['succeeded', 'failed', 'expired', 'cancelled'] as const satisfies readonly LocalaiCommandStatus[];
/** The statuses in which a command is "active": it holds its dedup key. */
export const ACTIVE_STATUSES = ['queued', 'running'] as const satisfies readonly LocalaiCommandStatus[];

/** The legal transitions. `running` can never go back to `queued` (at most once). */
export const COMMAND_TRANSITIONS: Readonly<Record<LocalaiCommandStatus, readonly LocalaiCommandStatus[]>> = {
  queued: ['running', 'expired', 'cancelled'],
  running: ['succeeded', 'failed'],
  succeeded: [],
  failed: [],
  expired: [],
  cancelled: [],
};

export function canTransition(from: LocalaiCommandStatus, to: LocalaiCommandStatus): boolean {
  return COMMAND_TRANSITIONS[from].includes(to);
}

/** The fields of a command row the time math reads. */
export interface LeaseFields {
  status: LocalaiCommandStatus;
  type: string;
  nodeId: string;
  claimToken: string | null;
  claimedAt: Date | null;
  leaseExpiresAt: Date | null;
}

/** The lease a claim granted at `now`. */
export function leaseExpiryAt(type: CommandType, now: Date): Date {
  return new Date(now.getTime() + policyOf(type).leaseMs);
}

/** The moment after which the command may no longer be renewed: `claimed_at + hardCap`. */
export function hardCapDeadline(type: CommandType, claimedAt: Date): Date {
  return new Date(claimedAt.getTime() + policyOf(type).hardCapMs);
}

/**
 * The lease after a renewal at `now`, or null when the hard cap has been reached. The lease is clamped to the hard
 * cap, so a command can never be kept alive past it by renewing late in its life.
 */
export function renewedLeaseExpiry(type: CommandType, claimedAt: Date, now: Date): Date | null {
  const deadline = hardCapDeadline(type, claimedAt);
  if (deadline.getTime() <= now.getTime()) return null;
  const next = leaseExpiryAt(type, now);
  return next.getTime() > deadline.getTime() ? deadline : next;
}

/** Whether `row` is a running command of `nodeId` holding `token` with an unexpired lease at `now`. */
export function isHeld(row: LeaseFields, nodeId: string, token: string, now: Date): boolean {
  return (
    row.status === 'running' &&
    row.nodeId === nodeId &&
    row.claimToken !== null &&
    row.claimToken === token &&
    row.leaseExpiresAt !== null &&
    row.leaseExpiresAt.getTime() > now.getTime()
  );
}

/** Whether a renewal at `now` is allowed: held, and the hard cap not reached. */
export function canRenew(row: LeaseFields, nodeId: string, token: string, now: Date): boolean {
  return (
    isHeld(row, nodeId, token, now) &&
    row.claimedAt !== null &&
    hardCapDeadline(row.type as CommandType, row.claimedAt).getTime() > now.getTime()
  );
}
