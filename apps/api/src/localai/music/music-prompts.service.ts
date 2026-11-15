// A person's saved music prompts (#568, docs/specs/cloud-app.md section 3.9): the history of what they typed, to
// edit, make again, save as new or delete. A prompt is read by its OWNER only (someone else's is 404, even for an
// administrator); a holder of `localai_music:delete` may also delete one. Deleting a prompt keeps its songs: their
// requests keep the title and settings they used, and `prompt_id` becomes NULL.
//
// The sample of a prompt is a REFERENCE to a stored object (the key the first request stored it under). "Make again"
// reuses it; "Save as new" copies the reference. The object is deleted only when no request and no prompt references
// it any more (`releaseSample`).
//
// Privacy: the title, description and lyrics are never logged or audited; audit rows carry ids only.
import { Inject, Injectable, Logger } from '@nestjs/common';
import type { LocalaiMusicPrompt, Prisma } from '@prisma/client';
import { AUDIT_SINK, type AuditEventInput, type AuditSink } from '@marinoscar/platform-api/core';

import { PrismaService } from '../../prisma/prisma.service';
import { LOCALAI_CLOCK, type LocalaiClock } from '../commands/command-events';
import { LOCALAI_PERMISSION_IDS } from '../localai.permissions';
import { AUDIT_MUSIC_PROMPT_CREATED, AUDIT_MUSIC_PROMPT_DELETED, AUDIT_MUSIC_PROMPT_UPDATED, AUDIT_TARGET_MUSIC_PROMPT } from './music-audit';
import { promptDeleteNotConfirmed, unknownPrompt } from './music-errors';
import { generatedTitle, type MusicLyricsMode, type UiLanguage } from './music-input';
import { MUSIC_OBJECT_STORE, type MusicObjectStore } from './music-store';
import { UUID, toPromptSummary, toPromptView, type MusicPromptSummary, type MusicPromptView, type Page } from './music.views';

/** A stored sample, as a prompt or a request references it. */
export interface StoredSample {
  key: string;
  mime: string;
  bytes: number;
  sha256: string;
}

/** What a prompt holds, as the services write it. */
export interface PromptFields {
  title: string;
  description: string;
  lyricsMode: MusicLyricsMode;
  lyrics: string;
  durationS: number;
  language: string;
  seed: number | null;
  variations: number;
  sampleMode: string | null;
  strength: number | null;
}

export interface PromptRequester {
  userId: string;
  permissions: readonly string[];
}

export const sampleOfPrompt = (row: Pick<LocalaiMusicPrompt, 'sampleKey' | 'sampleMime' | 'sampleBytes' | 'sampleSha256'>): StoredSample | null =>
  row.sampleKey && row.sampleMime && row.sampleBytes !== null && row.sampleSha256 ? { key: row.sampleKey, mime: row.sampleMime, bytes: row.sampleBytes, sha256: row.sampleSha256 } : null;

const sampleColumns = (sample: StoredSample | null) => ({
  sampleKey: sample?.key ?? null,
  sampleMime: sample?.mime ?? null,
  sampleBytes: sample?.bytes ?? null,
  sampleSha256: sample?.sha256 ?? null,
});

@Injectable()
export class MusicPromptsService {
  private readonly logger = new Logger(MusicPromptsService.name);

  constructor(
    private readonly prisma: PrismaService,
    @Inject(MUSIC_OBJECT_STORE) private readonly store: MusicObjectStore,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  /** `GET /prompts`: the caller's own prompts, newest used first, with how many ready songs each made. */
  async list(query: { page: number; pageSize: number }, userId: string): Promise<Page<MusicPromptSummary>> {
    const where: Prisma.LocalaiMusicPromptWhereInput = { ownerId: userId };
    const [total, rows] = await Promise.all([
      this.prisma.localaiMusicPrompt.count({ where }),
      this.prisma.localaiMusicPrompt.findMany({
        where,
        orderBy: [{ lastUsedAt: 'desc' }, { createdAt: 'desc' }, { id: 'desc' }],
        skip: (query.page - 1) * query.pageSize,
        take: query.pageSize,
      }),
    ]);
    const counts = await this.songCounts(rows.map((row) => row.id));
    return {
      items: rows.map((row) => toPromptSummary(row, counts.get(row.id) ?? 0)),
      page: query.page,
      pageSize: query.pageSize,
      total,
      totalPages: Math.ceil(total / query.pageSize),
    };
  }

  /** `GET /prompts/:id`: the caller's own prompt, with its lyrics. Anyone else's is 404. */
  async get(id: string, userId: string): Promise<MusicPromptView> {
    const row = await this.owned(id, userId);
    return toPromptView(row, (await this.songCounts([row.id])).get(row.id) ?? 0);
  }

  /** The caller's own prompt row, or 404. */
  async owned(id: string, userId: string): Promise<LocalaiMusicPrompt> {
    if (!UUID.test(id)) throw unknownPrompt();
    const row = await this.prisma.localaiMusicPrompt.findUnique({ where: { id } });
    if (!row || row.ownerId !== userId) throw unknownPrompt();
    return row;
  }

  /** `POST /prompts` (and the automatic prompt of `POST /requests`): a new prompt, audited `prompt_created`. */
  async create(fields: PromptFields, sample: StoredSample | null, userId: string, source: 'saved' | 'request'): Promise<LocalaiMusicPrompt> {
    const now = this.clock.now();
    const row = await this.prisma.localaiMusicPrompt.create({
      data: { ownerId: userId, ...this.columns(fields), ...sampleColumns(sample), lastUsedAt: now },
    });
    await this.recordAudit({ action: AUDIT_MUSIC_PROMPT_CREATED, actorUserId: userId, targetType: AUDIT_TARGET_MUSIC_PROMPT, targetId: row.id, meta: { source, hasSample: sample !== null } });
    return row;
  }

  /** `POST /prompts` from the route: the copy of another prompt's sample ("Save as new") is the caller's own. */
  async createFromBody(
    body: Omit<PromptFields, 'title' | 'seed' | 'sampleMode' | 'strength'> & { title?: string; seed?: number | null; sampleMode?: string | null; strength?: number | null; uiLanguage: UiLanguage; copySampleFrom?: string },
    userId: string,
  ): Promise<MusicPromptView> {
    const sample = body.copySampleFrom ? sampleOfPrompt(await this.owned(body.copySampleFrom, userId)) : null;
    const fields: PromptFields = {
      title: body.title && body.title !== '' ? body.title : generatedTitle(body.lyricsMode === 'instrumental' ? '' : body.lyrics, body.description, body.uiLanguage),
      description: body.description,
      lyricsMode: body.lyricsMode,
      lyrics: body.lyrics,
      durationS: body.durationS,
      language: body.language,
      seed: body.seed ?? null,
      variations: body.variations,
      sampleMode: sample ? (body.sampleMode ?? null) : null,
      strength: sample ? (body.strength ?? null) : null,
    };
    const row = await this.create(fields, sample, userId, 'saved');
    return toPromptView(row, 0);
  }

  /**
   * `PATCH /prompts/:id`: the caller's own prompt. An empty title is generated again from the (new) lyrics and
   * description. `removeSample` drops the reference (the object goes when nothing else references it). Past songs
   * never change: their requests keep their own copy of the title and settings.
   */
  async update(
    id: string,
    patch: Partial<Omit<PromptFields, 'seed' | 'sampleMode' | 'strength'>> & { seed?: number | null; sampleMode?: string | null; strength?: number | null; uiLanguage: UiLanguage; removeSample?: true },
    userId: string,
  ): Promise<MusicPromptView> {
    const row = await this.owned(id, userId);
    const merged = {
      description: patch.description ?? row.description,
      lyricsMode: (patch.lyricsMode ?? row.lyricsMode) as MusicLyricsMode,
      lyrics: patch.lyrics ?? row.lyrics,
    };
    const title =
      patch.title === undefined ? row.title : patch.title !== '' ? patch.title : generatedTitle(merged.lyricsMode === 'instrumental' ? '' : merged.lyrics, merged.description, patch.uiLanguage);
    const dropSample = patch.removeSample === true && row.sampleKey !== null;
    const data: Prisma.LocalaiMusicPromptUpdateInput = {
      title,
      ...merged,
      ...(patch.durationS !== undefined ? { durationS: patch.durationS } : {}),
      ...(patch.language !== undefined ? { language: patch.language } : {}),
      ...(patch.seed !== undefined ? { seed: patch.seed === null ? null : BigInt(patch.seed) } : {}),
      ...(patch.variations !== undefined ? { variations: patch.variations } : {}),
      ...(patch.sampleMode !== undefined ? { sampleMode: patch.sampleMode } : {}),
      ...(patch.strength !== undefined ? { strength: patch.strength } : {}),
      ...(dropSample ? { ...sampleColumns(null), sampleMode: null, strength: null } : {}),
      lastUsedAt: this.clock.now(),
    };
    const updated = await this.prisma.localaiMusicPrompt.update({ where: { id }, data });
    if (dropSample && row.sampleKey) await this.releaseSample(row.sampleKey);
    await this.recordAudit({ action: AUDIT_MUSIC_PROMPT_UPDATED, actorUserId: userId, targetType: AUDIT_TARGET_MUSIC_PROMPT, targetId: id, meta: { source: 'saved', removedSample: dropSample } });
    return toPromptView(updated, (await this.songCounts([id])).get(id) ?? 0);
  }

  /**
   * Saves what `POST /requests` was sent with `promptId` into that prompt (the song is made from what the person sees),
   * and its sample when one was uploaded or removed. Returns the key the prompt no longer references, if any, for the
   * caller to release once the request row (which may still reference it) exists.
   */
  async saveFromRequest(row: LocalaiMusicPrompt, fields: PromptFields, sample: StoredSample | null, userId: string): Promise<string | null> {
    const replaced = row.sampleKey !== null && row.sampleKey !== (sample?.key ?? null) ? row.sampleKey : null;
    await this.prisma.localaiMusicPrompt.update({
      where: { id: row.id },
      data: { ...this.columns(fields), ...sampleColumns(sample), lastUsedAt: this.clock.now() },
    });
    await this.recordAudit({ action: AUDIT_MUSIC_PROMPT_UPDATED, actorUserId: userId, targetType: AUDIT_TARGET_MUSIC_PROMPT, targetId: row.id, meta: { source: 'request', hasSample: sample !== null } });
    return replaced;
  }

  /** "Make again": the prompt was used now (it moves to the top of the history). */
  async touch(id: string): Promise<void> {
    await this.prisma.localaiMusicPrompt.updateMany({ where: { id }, data: { lastUsedAt: this.clock.now() } });
  }

  /**
   * `DELETE /prompts/:id` with `{ "confirm": "<prompt id>" }`: the owner, or a holder of `localai_music:delete`. The
   * songs stay (their requests' `prompt_id` becomes NULL). The sample object goes only when no request and no other
   * prompt references it. Audited `prompt_deleted` with ids only.
   */
  async delete(id: string, confirm: string, requester: PromptRequester): Promise<{ id: string; deleted: true }> {
    if (!UUID.test(id)) throw unknownPrompt();
    if (confirm !== id) throw promptDeleteNotConfirmed();
    const row = await this.prisma.localaiMusicPrompt.findUnique({ where: { id } });
    const admin = requester.permissions.includes(LOCALAI_PERMISSION_IDS.MUSIC_DELETE);
    if (!row || (row.ownerId !== requester.userId && !admin)) throw unknownPrompt();
    const songsKept = (await this.songCounts([id])).get(id) ?? 0;
    await this.prisma.localaiMusicPrompt.deleteMany({ where: { id } });
    if (row.sampleKey) await this.releaseSample(row.sampleKey);
    await this.recordAudit({
      action: AUDIT_MUSIC_PROMPT_DELETED,
      actorUserId: requester.userId,
      targetType: AUDIT_TARGET_MUSIC_PROMPT,
      targetId: id,
      meta: { ownerUserId: row.ownerId, songsKept },
    });
    return { id, deleted: true };
  }

  /**
   * Deletes a sample object once nothing references it: no request row and no prompt row names its key. Best effort:
   * a failed delete is logged (by error name) and the object stays, to be found by a storage sweep.
   */
  async releaseSample(key: string): Promise<boolean> {
    const [requests, prompts] = await Promise.all([
      this.prisma.localaiMusicRequest.count({ where: { sampleKey: key } }),
      this.prisma.localaiMusicPrompt.count({ where: { sampleKey: key } }),
    ]);
    if (requests > 0 || prompts > 0) return false;
    try {
      await this.store.delete(key);
      return true;
    } catch (error) {
      this.logger.warn(`could not delete an unused music sample (${error instanceof Error ? error.name : 'error'})`);
      return false;
    }
  }

  /** Ready songs per prompt id. */
  private async songCounts(promptIds: string[]): Promise<Map<string, number>> {
    const counts = new Map<string, number>();
    if (promptIds.length === 0) return counts;
    const requests = await this.prisma.localaiMusicRequest.findMany({
      where: { promptId: { in: promptIds } },
      select: { promptId: true, _count: { select: { songs: { where: { status: 'ready' } } } } },
    });
    for (const request of requests) {
      if (request.promptId) counts.set(request.promptId, (counts.get(request.promptId) ?? 0) + request._count.songs);
    }
    return counts;
  }

  private columns(fields: PromptFields) {
    return {
      title: fields.title,
      description: fields.description,
      lyricsMode: fields.lyricsMode,
      lyrics: fields.lyrics,
      durationS: fields.durationS,
      language: fields.language,
      seed: fields.seed === null ? null : BigInt(fields.seed),
      variations: fields.variations,
      sampleMode: fields.sampleMode,
      strength: fields.strength,
    };
  }

  private async recordAudit(event: AuditEventInput): Promise<void> {
    try {
      await this.audit.record(event);
    } catch (error) {
      this.logger.error(`audit ${event.action} failed for ${event.targetId} (${error instanceof Error ? error.name : 'error'})`);
    }
  }
}
