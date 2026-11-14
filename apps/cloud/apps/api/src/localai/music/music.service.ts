// The music feature of the cloud (E11, docs/specs/cloud-app.md sections 3.7 and 3.9, music.md section 4.7).
//
// A request is a `localai.music.generate` command for one node. The cloud never connects to a node (ADR-0003), so the
// bytes travel through object storage: the sample is stored here (`localai-music/samples/`), and when the node claims
// the command `MusicClaimHook` signs a GET of the sample and one PUT per song (`localai-music/songs/<request>/<i>`),
// valid for the command's hard cap. The stored params carry placeholders, so no signed URL is ever stored. The
// command's result then marks the songs ready.
//
// Status comes from the command. A settled command is reconciled by `MusicSettledListener` after the settling write
// commits; every read reconciles too, so a request whose event was lost (or a `running` claim, which publishes none)
// is still right on the next poll.
//
// Who sees what: a person sees their own requests and songs; a holder of `localai_music:delete` (org_admin) sees all.
// Lyrics: in the command's params only (the node needs them), returned to their author on the single-request route,
// never in a column, a log line, a span, an audit row or the command history (`redactParams`).
import { createHash, randomUUID } from 'node:crypto';
import { Inject, Injectable, Logger } from '@nestjs/common';
import type { LocalaiMusicRequest, Prisma } from '@prisma/client';
import { AUDIT_SINK, type AuditEventInput, type AuditSink } from '@marinoscar/platform-api/core';

import { PrismaService } from '../../prisma/prisma.service';
import type { CommandSource } from '../commands/command-enqueuer';
import { LOCALAI_CLOCK, type LocalaiClock } from '../commands/command-events';
import { commandForbidden, unknownLabNode } from '../commands/command-errors';
import { assertNodeTakesCommands } from '../commands/command-refusals';
import { CommandsService } from '../commands/commands.service';
import type { CommandParams } from '../contracts/commands';
import { LOCALAI_PERMISSION_IDS } from '../localai.permissions';
import { AUDIT_MUSIC_REQUEST_CANCELLED, AUDIT_MUSIC_REQUESTED, AUDIT_MUSIC_SONG_DELETED, AUDIT_TARGET_MUSIC_REQUEST, AUDIT_TARGET_MUSIC_SONG } from './music-audit';
import {
  musicRateLimited,
  musicRequestNotCancellable,
  musicStorageUnavailable,
  songDeleteNotConfirmed,
  unknownMusicRequest,
  unknownSong,
} from './music-errors';
import { extensionOf, songTitle, strengthFor, type MusicRequestFields, type SniffedAudio } from './music-input';
import { MUSIC_MAX_ACTIVE_PER_USER, MUSIC_MAX_PER_HOUR_PER_USER, MUSIC_PENDING_URL, MUSIC_PLAY_URL_TTL_S } from './music-limits';
import { MUSIC_OBJECT_STORE, MusicStorageNotConfigured, sampleKey, songKey, type MusicObjectStore } from './music-store';
import { MUSIC_ROLE, TERMINAL_REQUEST_STATUSES, activeWorkloadOf, musicReadiness, reconcileRequest, workloadsOf } from './music-state';
import {
  UUID,
  toLibrarySong,
  toRequestView,
  type MusicLibrarySong,
  type MusicNodeView,
  type MusicRequestView,
  type Page,
  type RequestRow,
} from './music.views';

/** Who asks: the user and the effective permissions of the credential. */
export interface MusicRequester {
  userId: string;
  permissions: readonly string[];
}

/** The sample as the controller read it: the bytes (at most 20 MiB) and their type, read from the bytes. */
export interface SampleInput {
  bytes: Buffer;
  audio: SniffedAudio;
}

const REQUEST_INCLUDE = { songs: true, node: { select: { nodeId: true, displayName: true } } } as const;
const HOUR_MS = 60 * 60 * 1000;

/** Whether the caller sees every person's requests and songs (the holders of `localai_music:delete`: org_admin). */
const seesAll = (requester: MusicRequester): boolean => requester.permissions.includes(LOCALAI_PERMISSION_IDS.MUSIC_DELETE);

@Injectable()
export class MusicService {
  private readonly logger = new Logger(MusicService.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly commands: CommandsService,
    @Inject(MUSIC_OBJECT_STORE) private readonly store: MusicObjectStore,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  /**
   * `POST /requests`. In order: the node exists (404), is enrolled (409 LAI-CLD-035) and not in Offline or Survival
   * mode (422 LAI-CLD-034); the person is within their limits (429 LAI-SVC-153); storage answers and signs https URLs
   * (503 otherwise); the sample is stored; one pending song row per variation; the command is queued with placeholder
   * URLs (signed at the claim); audited `localai.music.requested`.
   */
  async create(fields: MusicRequestFields, sample: SampleInput | null, requester: MusicRequester, source: CommandSource): Promise<MusicRequestView> {
    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId: fields.nodeId }, select: { id: true, nodeId: true, phase: true, mode: true } });
    if (!node) throw unknownLabNode();
    assertNodeTakesCommands(node);
    await this.assertWithinLimits(requester.userId);

    const requestId = randomUUID();
    const sampleObject = sample ? { key: sampleKey(requestId, sample.audio.ext), sha256: createHash('sha256').update(sample.bytes).digest('hex') } : null;
    const keys = Array.from({ length: fields.variations }, (_, index) => songKey(requestId, index));

    // Storage first: one that is not set up, or that would sign plain-http URLs the node refuses, ends the request here,
    // before any row exists. The probe signature is thrown away; the real ones are minted at the claim.
    try {
      const probe = await this.store.signedPut(keys[0], 60);
      if (!probe.startsWith('https://')) throw musicStorageUnavailable('not_https');
      if (sample && sampleObject) await this.store.put(sampleObject.key, sample.bytes, sample.audio.mime);
    } catch (error) {
      throw this.storageError(error);
    }

    const sampleMode = sample ? (fields.sample_mode ?? 'sounds_like') : undefined;
    const strength = strengthFor(sampleMode, fields.strength, sample !== null);
    const params: CommandParams<'localai.music.generate'> = {
      request_id: requestId,
      lyrics: fields.lyrics,
      tags: fields.tags,
      duration_s: fields.duration_s,
      language: fields.language,
      ...(fields.seed !== undefined ? { seed: fields.seed } : {}),
      variations: fields.variations,
      ...(sample && sampleObject ? { sample: { url: MUSIC_PENDING_URL, sha256: sampleObject.sha256, mime: sample.audio.mime, size_bytes: sample.bytes.length } } : {}),
      ...(sampleMode ? { sample_mode: sampleMode } : {}),
      ...(strength !== undefined ? { strength } : {}),
      results: keys.map(() => ({ put_url: MUSIC_PENDING_URL })),
    };

    await this.prisma.localaiMusicRequest.create({
      data: {
        id: requestId,
        nodeId: node.id,
        requestedById: requester.userId,
        status: 'queued',
        title: songTitle(fields.lyrics, fields.tags),
        tags: fields.tags,
        lyricsBytes: new TextEncoder().encode(fields.lyrics).length,
        durationS: fields.duration_s,
        language: fields.language,
        seed: fields.seed !== undefined ? BigInt(fields.seed) : null,
        variations: fields.variations,
        sampleMode: sampleMode ?? null,
        strength: strength ?? null,
        sampleKey: sampleObject?.key ?? null,
        sampleMime: sample?.audio.mime ?? null,
        sampleBytes: sample?.bytes.length ?? null,
        sampleSha256: sampleObject?.sha256 ?? null,
        songs: { create: keys.map((storageKey, index) => ({ index, storageKey, status: 'pending' as const })) },
      },
    });

    let commandId: string;
    try {
      const { command } = await this.commands.enqueueForNode(node.id, 'localai.music.generate', params, { userId: requester.userId }, source);
      commandId = command.id;
    } catch (error) {
      // Nothing was queued: the request ends here, with no code (the cloud refused it, not the node).
      await this.prisma.localaiMusicRequest.updateMany({
        where: { id: requestId },
        data: { status: 'failed', errorMessage: 'The request could not be queued. Try again.', finishedAt: this.clock.now() },
      });
      await this.prisma.localaiMusicSong.updateMany({ where: { requestId, status: 'pending' }, data: { status: 'failed' } });
      if (sampleObject) await this.forget(sampleObject.key);
      throw error;
    }
    await this.prisma.localaiMusicRequest.update({ where: { id: requestId }, data: { commandId } });

    this.logger.log(
      `music request ${requestId} queued for ${node.nodeId}: command_id=${commandId} variations=${fields.variations} lyrics_bytes=${new TextEncoder().encode(fields.lyrics).length} sample_bytes=${sample?.bytes.length ?? 0}`,
    );
    await this.recordAudit({
      action: AUDIT_MUSIC_REQUESTED,
      actorUserId: requester.userId,
      targetType: AUDIT_TARGET_MUSIC_REQUEST,
      targetId: requestId,
      meta: {
        nodeId: node.nodeId,
        commandId,
        variations: fields.variations,
        durationS: fields.duration_s,
        hasSample: sample !== null,
        ...(sampleMode ? { sampleMode } : {}),
        ...(sample ? { sampleBytes: sample.bytes.length } : {}),
      },
    });
    return this.getRequest(requestId, requester);
  }

  /**
   * `GET /requests`: newest first. A person sees their own; a holder of `localai_music:delete` sees every request
   * (or only theirs with `mine=true`).
   */
  async listRequests(query: { page: number; pageSize: number; mine?: boolean; active?: boolean }, requester: MusicRequester): Promise<Page<MusicRequestView>> {
    const onlyMine = query.mine === true || !seesAll(requester);
    const where: Prisma.LocalaiMusicRequestWhereInput = {
      ...(onlyMine ? { requestedById: requester.userId } : {}),
      ...(query.active === true ? { status: { in: ['queued', 'running'] } } : query.active === false ? { status: { notIn: ['queued', 'running'] } } : {}),
    };
    const [total, rows] = await Promise.all([
      this.prisma.localaiMusicRequest.count({ where }),
      this.prisma.localaiMusicRequest.findMany({ where, include: REQUEST_INCLUDE, orderBy: [{ createdAt: 'desc' }, { id: 'desc' }], skip: (query.page - 1) * query.pageSize, take: query.pageSize }),
    ]);
    const fresh = await this.refresh(rows as RequestRow[]);
    return { items: fresh.map((row) => toRequestView(row, requester.userId)), page: query.page, pageSize: query.pageSize, total, totalPages: Math.ceil(total / query.pageSize) };
  }

  /** `GET /requests/:id`: one request with its songs (and, for its author, the lyrics). Someone else's is 404. */
  async getRequest(id: string, requester: MusicRequester): Promise<MusicRequestView> {
    if (!UUID.test(id)) throw unknownMusicRequest();
    const row = (await this.prisma.localaiMusicRequest.findUnique({ where: { id }, include: REQUEST_INCLUDE })) as RequestRow | null;
    if (!row || (row.requestedById !== requester.userId && !seesAll(requester))) throw unknownMusicRequest();
    const [fresh] = await this.refresh([row]);
    const view = toRequestView(fresh, requester.userId);
    if (view.requestedByMe && fresh.commandId) {
      const command = await this.prisma.localaiCommand.findUnique({ where: { id: fresh.commandId }, select: { params: true } });
      const lyrics = (command?.params as { lyrics?: unknown } | null | undefined)?.lyrics;
      if (typeof lyrics === 'string') view.lyrics = lyrics;
    }
    return view;
  }

  /**
   * `POST /requests/:id/cancel`: a request still `queued` (no node has claimed it) is cancelled through the command
   * plane; a running one cannot be stopped from the cloud (409). Only the person who asked, or a holder of
   * `localai_music:delete`, may cancel.
   */
  async cancelRequest(id: string, requester: MusicRequester): Promise<MusicRequestView> {
    if (!UUID.test(id)) throw unknownMusicRequest();
    const owner = await this.prisma.localaiMusicRequest.findUnique({ where: { id }, select: { requestedById: true } });
    if (!owner) throw unknownMusicRequest();
    if (owner.requestedById !== requester.userId && !seesAll(requester)) throw commandForbidden(LOCALAI_PERMISSION_IDS.MUSIC_DELETE);
    const view = await this.getRequest(id, requester);
    if (view.status !== 'queued' || view.commandId === null) throw musicRequestNotCancellable(view.status);
    try {
      // The command's own permission is localai_music:use, which the route already required.
      await this.commands.cancel(view.commandId, { userId: requester.userId, permissions: [...requester.permissions, LOCALAI_PERMISSION_IDS.MUSIC_USE] });
    } catch (error) {
      // The node claimed it a moment ago: answer with the request's current status.
      const status = (error as { getResponse?: () => { details?: { status?: string } } }).getResponse?.().details?.status;
      if (typeof status === 'string') throw musicRequestNotCancellable(status);
      throw error;
    }
    await this.recordAudit({
      action: AUDIT_MUSIC_REQUEST_CANCELLED,
      actorUserId: requester.userId,
      targetType: AUDIT_TARGET_MUSIC_REQUEST,
      targetId: id,
      meta: { nodeId: view.nodeId, commandId: view.commandId },
    });
    return this.getRequest(id, requester);
  }

  /** `GET /songs`: the ready songs the caller may see (their own; all for `localai_music:delete`), newest first. */
  async listSongs(query: { page: number; pageSize: number; nodeId?: string; mine?: boolean }, requester: MusicRequester): Promise<Page<MusicLibrarySong>> {
    // Requests the page has stopped polling still become songs here.
    await this.refreshActive();
    const onlyMine = query.mine === true || !seesAll(requester);
    const where: Prisma.LocalaiMusicSongWhereInput = {
      status: 'ready',
      request: {
        ...(query.nodeId ? { node: { nodeId: query.nodeId } } : {}),
        ...(onlyMine ? { requestedById: requester.userId } : {}),
      },
    };
    const [total, rows] = await Promise.all([
      this.prisma.localaiMusicSong.count({ where }),
      this.prisma.localaiMusicSong.findMany({
        where,
        include: { request: { include: { node: { select: { nodeId: true, displayName: true } } } } },
        orderBy: [{ readyAt: 'desc' }, { createdAt: 'desc' }, { id: 'desc' }],
        skip: (query.page - 1) * query.pageSize,
        take: query.pageSize,
      }),
    ]);
    return {
      items: rows.map((row) => toLibrarySong(row, requester.userId)),
      page: query.page,
      pageSize: query.pageSize,
      total,
      totalPages: Math.ceil(total / query.pageSize),
    };
  }

  /** `GET /songs/:id/audio`: a 5-minute signed GET of a ready song the caller may see, inline or as an attachment. */
  async audioUrl(songId: string, download: boolean, requester: MusicRequester): Promise<{ url: string; expiresAt: string }> {
    if (!UUID.test(songId)) throw unknownSong();
    const song = await this.prisma.localaiMusicSong.findUnique({ where: { id: songId }, include: { request: { select: { title: true, requestedById: true } } } });
    if (!song || song.status !== 'ready' || (song.request.requestedById !== requester.userId && !seesAll(requester))) throw unknownSong();
    const filename = `${song.request.title} ${song.index + 1}.${extensionOf(song.mime)}`;
    let url: string;
    try {
      url = await this.store.signedGet(song.storageKey, MUSIC_PLAY_URL_TTL_S, { filename, attachment: download });
    } catch (error) {
      throw this.storageError(error);
    }
    return { url, expiresAt: new Date(this.clock.now().getTime() + MUSIC_PLAY_URL_TTL_S * 1000).toISOString() };
  }

  /**
   * `DELETE /songs/:id` with `{ "confirm": "<song id>" }`: the cloud copy only (the node keeps its own). The object
   * first, then the row (a failed object delete keeps the row, so nothing is orphaned unseen). The last song of a
   * finished request also takes its sample.
   */
  async deleteSong(songId: string, confirm: string, requester: MusicRequester): Promise<{ id: string; deleted: true }> {
    if (!UUID.test(songId)) throw unknownSong();
    if (confirm !== songId) throw songDeleteNotConfirmed();
    const song = await this.prisma.localaiMusicSong.findUnique({
      where: { id: songId },
      include: { request: { select: { id: true, status: true, sampleKey: true, node: { select: { nodeId: true } } } } },
    });
    if (!song) throw unknownSong();
    try {
      await this.store.delete(song.storageKey);
    } catch (error) {
      throw this.storageError(error);
    }
    await this.prisma.localaiMusicSong.deleteMany({ where: { id: songId } });

    const remaining = await this.prisma.localaiMusicSong.count({ where: { requestId: song.requestId } });
    if (remaining === 0 && song.request.sampleKey && TERMINAL_REQUEST_STATUSES.has(song.request.status)) {
      await this.forget(song.request.sampleKey);
      await this.prisma.localaiMusicRequest.updateMany({ where: { id: song.requestId }, data: { sampleKey: null } });
    }
    await this.recordAudit({
      action: AUDIT_MUSIC_SONG_DELETED,
      actorUserId: requester.userId,
      targetType: AUDIT_TARGET_MUSIC_SONG,
      targetId: songId,
      meta: { nodeId: song.request.node.nodeId, requestId: song.requestId, index: song.index },
    });
    return { id: songId, deleted: true };
  }

  /** `GET /nodes`: every enrolled node with its music readiness (state first) and its workloads from its snapshot. */
  async nodes(): Promise<MusicNodeView[]> {
    const now = this.clock.now();
    const nodes = await this.prisma.localaiNode.findMany({
      where: { phase: 'enrolled' },
      select: { id: true, nodeId: true, displayName: true, phase: true, mode: true, lastStateAt: true, lastState: true },
      orderBy: { nodeId: 'asc' },
    });
    if (nodes.length === 0) return [];
    const ids = nodes.map((node) => node.id);
    const [models, switches] = await Promise.all([
      this.prisma.localaiNodeModel.findMany({
        where: { nodeId: { in: ids }, roles: { has: MUSIC_ROLE } },
        select: { nodeId: true, modelId: true, present: true, loadable: true, loadableReason: true, status: true, roles: true },
      }),
      this.prisma.localaiCommand.findMany({
        where: { nodeId: { in: ids }, type: 'localai.workload.activate', status: 'succeeded' },
        select: { nodeId: true, result: true, finishedAt: true },
        orderBy: { finishedAt: 'desc' },
      }),
    ]);
    return nodes.map((node) => {
      const mine = models.filter((model) => model.nodeId === node.id);
      const last = switches.find((entry) => entry.nodeId === node.id);
      const workloads = workloadsOf(node.lastState);
      return {
        nodeId: node.nodeId,
        nodeName: node.displayName || node.nodeId,
        readiness: musicReadiness(node, mine, now, workloads),
        musicModels: [...mine].sort((a, b) => Number(b.status === 'loaded') - Number(a.status === 'loaded')).map((model) => model.modelId),
        activeWorkload: workloads ? workloads.active : last ? activeWorkloadOf(last.result) : null,
        workloads: workloads ? workloads.items.map((item) => ({ name: item.name, label: item.label, fits: item.fits, missing: item.missing })) : null,
        lastStateAt: node.lastStateAt ? node.lastStateAt.toISOString() : null,
      };
    });
  }

  /**
   * Brings a request in line with its command (called by the settled listener and by every read). Writes only when the
   * status changes, conditionally on the stored one, so a listener and a poll racing write once.
   */
  async reconcileByCommand(commandId: string): Promise<void> {
    const row = await this.prisma.localaiMusicRequest.findUnique({ where: { commandId }, include: REQUEST_INCLUDE });
    if (row) await this.refresh([row as RequestRow]);
  }

  private async refreshActive(): Promise<void> {
    const rows = await this.prisma.localaiMusicRequest.findMany({ where: { status: { in: ['queued', 'running'] } }, include: REQUEST_INCLUDE, take: 200 });
    await this.refresh(rows as RequestRow[]);
  }

  private async refresh(rows: RequestRow[]): Promise<RequestRow[]> {
    const open = rows.filter((row) => !TERMINAL_REQUEST_STATUSES.has(row.status) && row.commandId !== null);
    if (open.length === 0) return rows;
    const commands = await this.prisma.localaiCommand.findMany({
      where: { id: { in: open.map((row) => row.commandId as string) } },
      select: { id: true, status: true, result: true, error: true, finishedAt: true },
    });
    const byId = new Map(commands.map((command) => [command.id, command]));
    const out: RequestRow[] = [];
    for (const row of rows) {
      const command = row.commandId ? byId.get(row.commandId) : undefined;
      if (!command || TERMINAL_REQUEST_STATUSES.has(row.status)) {
        out.push(row);
        continue;
      }
      const next = reconcileRequest(command);
      if (next.status === row.status) {
        out.push(row);
        continue;
      }
      const terminal = TERMINAL_REQUEST_STATUSES.has(next.status);
      const finishedAt = terminal ? (command.finishedAt ?? this.clock.now()) : null;
      const data: Prisma.LocalaiMusicRequestUpdateManyMutationInput = {
        status: next.status,
        ...(terminal ? { finishedAt } : {}),
        ...(next.error ? { errorCode: next.error.code, errorMessage: next.error.message } : {}),
      };
      // The songs first, then the request: a reader that sees the request finished also sees its songs. Both steps
      // are idempotent (each write is conditional on `pending`, and the outcome comes from the same settled command),
      // so the listener and a poll racing here end in the same rows.
      if (terminal) {
        for (const song of next.ready) {
          await this.prisma.localaiMusicSong.updateMany({
            where: { requestId: row.id, index: song.index, status: 'pending' },
            data: { status: 'ready', sha256: song.sha256, sizeBytes: song.sizeBytes, mime: song.mime, durationS: song.durationS, readyAt: finishedAt },
          });
        }
        await this.prisma.localaiMusicSong.updateMany({ where: { requestId: row.id, status: 'pending' }, data: { status: 'failed' } });
      }
      const written = await this.prisma.localaiMusicRequest.updateMany({ where: { id: row.id, status: row.status }, data });
      if (written.count === 1 && terminal) {
        this.logger.log(`music request ${row.id} ${next.status}: songs_ready=${next.ready.length}${next.error?.code ? ` code=${next.error.code}` : ''}`);
      }
      const reread = (await this.prisma.localaiMusicRequest.findUnique({ where: { id: row.id }, include: REQUEST_INCLUDE })) as RequestRow | null;
      out.push(reread ?? { ...row, ...(data as Partial<LocalaiMusicRequest>) });
    }
    return out;
  }

  /** Per person: at most 3 requests waiting or running, at most 30 in a rolling hour (429 LAI-SVC-153). */
  private async assertWithinLimits(userId: string): Promise<void> {
    const active = await this.prisma.localaiMusicRequest.findMany({ where: { requestedById: userId, status: { in: ['queued', 'running'] } }, include: REQUEST_INCLUDE });
    const fresh = await this.refresh(active as RequestRow[]);
    if (fresh.filter((row) => row.status === 'queued' || row.status === 'running').length >= MUSIC_MAX_ACTIVE_PER_USER) throw musicRateLimited('too_many_active', 60);
    const since = new Date(this.clock.now().getTime() - HOUR_MS);
    const recent = await this.prisma.localaiMusicRequest.count({ where: { requestedById: userId, createdAt: { gte: since } } });
    if (recent >= MUSIC_MAX_PER_HOUR_PER_USER) throw musicRateLimited('too_many_per_hour', 600);
  }

  private storageError(error: unknown): Error {
    if (error instanceof MusicStorageNotConfigured) return musicStorageUnavailable('not_configured');
    if (error instanceof Error && (error as { getStatus?: () => number }).getStatus?.() === 503) return error;
    this.logger.error(`object storage call failed (${error instanceof Error ? error.name : 'error'})`);
    return musicStorageUnavailable('failed');
  }

  /** Best-effort delete of an object nobody will reference (a sample of a refused request). Logged, never raised. */
  private async forget(key: string): Promise<void> {
    try {
      await this.store.delete(key);
    } catch (error) {
      this.logger.warn(`could not delete an unused music object (${error instanceof Error ? error.name : 'error'})`);
    }
  }

  private async recordAudit(event: AuditEventInput): Promise<void> {
    try {
      await this.audit.record(event);
    } catch (error) {
      this.logger.error(`audit ${event.action} failed for ${event.targetId} (${error instanceof Error ? error.name : 'error'})`);
    }
  }
}
