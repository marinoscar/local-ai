// The derived states of the music feature, as PURE functions (no I/O, no clock), so the tables are tested whole:
//   - a node's music readiness, from its last snapshot (the page's node picker: state first),
//   - the node's workloads (its snapshot's `workloads` block) and the one it last switched to,
//   - a request's status, reconciled from the command that carries it.
import type { LocalaiCommandStatus, LocalaiMusicRequestStatus } from '@prisma/client';

import { COMMANDS } from '../contracts/commands';
import { WorkloadsBlockV1 } from '../contracts/state.schema';
import { DEGRADED_MAX_AGE_SECONDS, ageSeconds } from '../health/derive-health';

/** The catalog role of a music model (E11 contract section 1). */
export const MUSIC_ROLE = 'music';
/** The workload that makes music (E11 contract section 4: a node.yaml adds it once the catalog entry exists). */
export const MUSIC_WORKLOAD = 'music';

/**
 * What the picker shows for a node:
 *   ready            the node is on the Music workload: make music now
 *   can_load         it has a Music workload that fits: one press of "Switch to Music"
 *   too_little_vram  its Music workload does not fit this GPU
 *   offline          the node is retired or pending, in Offline or Survival mode, or silent for over 10 minutes
 *   no_music         no Music workload, or its models are not on the node
 */
export type MusicReadiness = 'ready' | 'can_load' | 'too_little_vram' | 'offline' | 'no_music';

export interface ReadinessNode {
  phase: string;
  mode: string | null;
  lastStateAt: Date | null;
}

export interface ReadinessModel {
  modelId: string;
  present: boolean;
  loadable: boolean;
  loadableReason: string | null;
  status: string;
  roles: readonly string[];
}

/** A not-loadable reason that is about the GPU's memory (the agent's text: "needs 6144 MiB VRAM", "does not fit"). */
const VRAM_REASON = /vram|gpu|fit|memory/i;

/** The node's `workloads` block from its last snapshot (strictly parsed), or null for an agent that sends none. */
export function workloadsOf(lastState: unknown): WorkloadsBlockV1 | null {
  if (lastState === null || typeof lastState !== 'object') return null;
  const parsed = WorkloadsBlockV1.safeParse((lastState as Record<string, unknown>).workloads);
  return parsed.success ? parsed.data : null;
}

/**
 * The node's music readiness. With a `workloads` block (an agent with `workloads.v1`) it is read from the `music`
 * workload: active = ready, missing models = no_music, fits = can_load, does not fit = too_little_vram. Without one (an
 * older agent) it falls back to the models whose catalog role is `music`.
 */
export function musicReadiness(node: ReadinessNode, models: readonly ReadinessModel[], now: Date, workloads: WorkloadsBlockV1 | null = null): MusicReadiness {
  if (node.phase !== 'enrolled' || node.mode === 'offline' || node.mode === 'survival') return 'offline';
  if (ageSeconds(node.lastStateAt, now) > DEGRADED_MAX_AGE_SECONDS) return 'offline';
  if (workloads) {
    const item = workloads.items.find((candidate) => candidate.name === MUSIC_WORKLOAD);
    if (!item) return 'no_music';
    if (workloads.active === MUSIC_WORKLOAD) return 'ready';
    if (item.missing.length > 0) return 'no_music';
    return item.fits ? 'can_load' : 'too_little_vram';
  }
  const music = models.filter((model) => model.roles.includes(MUSIC_ROLE));
  if (music.some((model) => model.status === 'loaded' || model.status === 'loading')) return 'ready';
  const present = music.filter((model) => model.present);
  if (present.some((model) => model.loadable)) return 'can_load';
  if (present.some((model) => !model.loadable && (model.loadableReason === null || VRAM_REASON.test(model.loadableReason)))) return 'too_little_vram';
  return 'no_music';
}

/** The workload a node last switched to through the cloud: the result of its newest succeeded `workload.activate`. */
export function activeWorkloadOf(result: unknown): string | null {
  const parsed = COMMANDS['localai.workload.activate'].result.safeParse(result);
  return parsed.success ? parsed.data.active : null;
}

/** The request status for a command status (a command that no longer exists leaves the request as it is). */
export function requestStatusFor(command: LocalaiCommandStatus): LocalaiMusicRequestStatus {
  return command;
}

export const TERMINAL_REQUEST_STATUSES: ReadonlySet<LocalaiMusicRequestStatus> = new Set(['succeeded', 'failed', 'expired', 'cancelled']);

/** One song's update from a succeeded command's result. */
export interface SongOutcome {
  index: number;
  sha256: string;
  sizeBytes: number;
  mime: string;
  durationS: number;
}

export interface Reconciled {
  status: LocalaiMusicRequestStatus;
  /** The songs the node uploaded (a `succeeded` command); empty otherwise. */
  ready: SongOutcome[];
  error: { code: string | null; message: string | null } | null;
}

/**
 * What a request becomes from its command. A succeeded result that does not parse (it was validated when the node
 * posted it, so this means a newer schema) marks the request failed rather than inventing songs; a succeeded result
 * with no song at all is failed too (nothing to play).
 */
export function reconcileRequest(command: { status: LocalaiCommandStatus; result: unknown; error: unknown }): Reconciled {
  if (command.status === 'succeeded') {
    const parsed = COMMANDS['localai.music.generate'].result.safeParse(command.result);
    if (!parsed.success) return { status: 'failed', ready: [], error: { code: null, message: 'The computer sent a result this cloud cannot read.' } };
    if (parsed.data.songs.length === 0) return { status: 'failed', ready: [], error: { code: null, message: 'The computer finished without a song.' } };
    return {
      status: 'succeeded',
      ready: parsed.data.songs.map((song) => ({ index: song.index, sha256: song.sha256, sizeBytes: song.size_bytes, mime: song.mime, durationS: song.duration_s })),
      error: null,
    };
  }
  if (command.status === 'failed' || command.status === 'expired') {
    const error = (command.error ?? null) as { code?: unknown; message?: unknown } | null;
    const code = typeof error?.code === 'string' ? error.code : null;
    const message =
      typeof error?.message === 'string'
        ? error.message
        : command.status === 'expired'
          ? 'The computer did not pick up this request in time. Check that it is on and online, then try again.'
          : null;
    return { status: command.status, ready: [], error: { code, message } };
  }
  return { status: requestStatusFor(command.status), ready: [], error: null };
}

/** The progress line the page shows: queued on the node, generating, done, or how it ended. */
export type MusicStage = 'queued' | 'generating' | 'done' | 'failed' | 'expired' | 'cancelled';

export function stageOf(status: LocalaiMusicRequestStatus): MusicStage {
  switch (status) {
    case 'running':
      return 'generating';
    case 'succeeded':
      return 'done';
    default:
      return status;
  }
}
