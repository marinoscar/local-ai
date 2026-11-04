// The two events of the command plane on the platform event bus (docs/specs/cloud-app.md sections 4.4 and 7), and the
// clock the plane reads. `nodeId` in both payloads is the `localai_nodes.id` uuid (the same value as
// `localai_commands.node_id`), not the `ai-nN` string.
import type { LocalaiCommandStatus } from '@prisma/client';

/** A command was queued for a node: wakes that node's long-poll claim. */
export const COMMAND_ENQUEUED_CHANNEL = 'localai.command.enqueued';
/** A command reached a terminal state through a result, a failure or the sweeper. */
export const COMMAND_SETTLED_CHANNEL = 'localai.command.settled';

export interface CommandEnqueuedEvent {
  nodeId: string;
}

export interface CommandSettledEvent {
  nodeId: string;
  commandId: string;
  status: Extract<LocalaiCommandStatus, 'succeeded' | 'failed' | 'expired'>;
}

/** The clock of the command plane: every lease, expiry and hard cap is computed from it (the cloud's time). */
export interface LocalaiClock {
  now(): Date;
}

/** Injection token of the clock; the default is the system clock, a test binds a fake one. */
export const LOCALAI_CLOCK = Symbol('LOCALAI_CLOCK');

export const systemLocalaiClock: LocalaiClock = { now: () => new Date() };
