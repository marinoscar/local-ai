// Mints a music request's signed URLs at the moment a node claims its command (E11; `commands/claim-params.hooks.ts`).
// The stored params hold `MUSIC_PENDING_URL` placeholders; here the sample's GET and one PUT per song are signed for
// the command's hard cap, for keys the cloud chose and stored when the request was made (never from the params). The
// URLs go to the node in the claim answer only: they are not stored, logged or audited.
import { Inject, Injectable, Logger, type OnModuleInit } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { ClaimParamsHooks } from '../commands/claim-params.hooks';
import { MUSIC_NODE_URL_TTL_S } from './music-limits';
import { MUSIC_OBJECT_STORE, type MusicObjectStore } from './music-store';

@Injectable()
export class MusicClaimHook implements OnModuleInit {
  private readonly logger = new Logger(MusicClaimHook.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly hooks: ClaimParamsHooks,
    @Inject(MUSIC_OBJECT_STORE) private readonly store: MusicObjectStore,
  ) {}

  onModuleInit(): void {
    this.hooks.register('localai.music.generate', (params, commandId) => this.sign(params, commandId));
  }

  /** The params with real signed URLs. Throws (the hook seam then keeps the placeholders) when the request is unknown. */
  async sign(params: Record<string, unknown>, commandId: string): Promise<Record<string, unknown>> {
    const requestId = typeof params.request_id === 'string' ? params.request_id : '';
    const request = await this.prisma.localaiMusicRequest.findUnique({
      where: { id: requestId },
      select: { id: true, sampleKey: true, songs: { select: { index: true, storageKey: true }, orderBy: { index: 'asc' } } },
    });
    if (!request) throw new Error('music request not found');
    const results = Array.isArray(params.results) ? params.results : [];
    if (request.songs.length !== results.length) throw new Error('music request songs do not match the params');

    const putUrls = await Promise.all(request.songs.map((song) => this.store.signedPut(song.storageKey, MUSIC_NODE_URL_TTL_S)));
    const signed: Record<string, unknown> = { ...params, results: putUrls.map((put_url) => ({ put_url })) };
    if (params.sample && typeof params.sample === 'object') {
      if (!request.sampleKey) throw new Error('music request has no stored sample');
      signed.sample = { ...(params.sample as Record<string, unknown>), url: await this.store.signedGet(request.sampleKey, MUSIC_NODE_URL_TTL_S) };
    }
    this.logger.log(`music request ${request.id} signed for command_id=${commandId}: puts=${putUrls.length} sample=${signed.sample ? 'yes' : 'no'} ttl_s=${MUSIC_NODE_URL_TTL_S}`);
    return signed;
  }
}
